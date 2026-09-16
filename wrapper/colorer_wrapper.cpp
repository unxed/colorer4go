#include "colorer_wrapper.h"
#include <colorer/ParserFactory.h>
#include <colorer/TextParser.h>
#include <colorer/LineSource.h>
#include <colorer/handlers/LineRegionsSupport.h>
#include <colorer/common/Logger.h>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <vector>
#include <string>
#include <unordered_map>
#include <utility>

// ----------------------------------------------------------------------
// Diagnostics
// ----------------------------------------------------------------------
//
// Two functions the Go host provides in the "colorer4go" import module.
// Strings are passed as pointer and length into linear memory and are only
// valid for the duration of the call.
extern "C" {
// One message Colorer reported through its Logger interface.
__attribute__((import_module("colorer4go"), import_name("log")))
void colorer4go_host_log(int32_t level, const char* file, int32_t file_len, int32_t line,
                         const char* func, int32_t func_len, const char* msg, int32_t msg_len);
// Why the current call is about to abort. Called at most once per call, right
// before the trap the host then sees as the call's error.
__attribute__((import_module("colorer4go"), import_name("fatal")))
void colorer4go_host_fatal(const char* msg, int32_t msg_len);
}

static int32_t c_len(const char* s) {
    return s ? static_cast<int32_t>(strlen(s)) : 0;
}

// Forwards Colorer's own diagnostics — the Logger interface far2l's
// CerrLogger implements — to the host. Colorer checks getCurrentLogLevel()
// before it formats a message, so the level set here keeps filtered messages
// from costing anything.
class HostLogger final : public Logger {
public:
    LogLevel level = LOG_OFF;

    void log(LogLevel lvl, const char* file, int line, const char* func, const char* message) override {
        colorer4go_host_log(static_cast<int32_t>(lvl), file, c_len(file), line,
                            func, c_len(func), message, c_len(message));
    }
    void flush() override {}
    LogLevel getCurrentLogLevel() override { return level; }
};

static HostLogger host_logger;

extern "C" [[noreturn]] void colorer4go_throw_abort(const char* file, int line, const char* func) noexcept {
    char msg[512];
    int n = snprintf(msg, sizeof(msg), "C++ exception thrown at %s:%d in %s()",
                     file ? file : "?", line, func ? func : "?");
    if (n < 0) {
        n = 0;
    } else if (n >= static_cast<int>(sizeof(msg))) {
        n = sizeof(msg) - 1;
    }
    colorer4go_host_fatal(msg, n);
    abort();
}

struct WasmRegion {
    int start;
    int end;
    const char* name;
    unsigned int fore;
    unsigned int back;
    unsigned int style;
    int isForeSet;
    int isBackSet;
};

// Sliding window of the lines fed to the session.
//
// Lines are numbered absolutely, from the first line ever fed after a reset:
// `base` is the number of the oldest line still stored, so line `lno` lives at
// `lines[lno - base]`. Trimming the front of the window is what keeps a long
// forward scroll from accumulating the whole file in wasm memory as UTF-16.
//
// Safe to trim because the parser only ever reads forward: TextParser::parse()
// positions itself through the parse cache (which keeps its own copy of the
// line that opened a block, see ParseCache::backLine) and colorize() then walks
// getLine() from `from` upwards. A line below the next line to be parsed will
// not be asked for again. Asking for a dropped line is fatal, not merely wrong:
// getLine() returning nullptr makes the parser throw, and with -fno-exceptions
// that is abort() and a trapped module.
class WasmLineSource : public LineSource {
public:
    std::deque<UnicodeString> lines;
    size_t base = 0;

    // Number of the oldest line still stored.
    size_t firstLine() const { return base; }
    // Number the next line fed to the session will get.
    size_t nextLine() const { return base + lines.size(); }

    void append(UnicodeString&& line) {
        lines.push_back(std::move(line));
    }

    // Drops the stored text of every line below `lno`. Idempotent, and clamped
    // at both ends: lines already dropped and lines not yet fed are ignored.
    void forgetBefore(size_t lno) {
        if (lno <= base) return;
        size_t drop = lno - base;
        if (drop > lines.size()) drop = lines.size();
        lines.erase(lines.begin(), lines.begin() + static_cast<ptrdiff_t>(drop));
        base += drop;
    }

    void clear() {
        lines.clear();
        base = 0;
    }

    UnicodeString* getLine(size_t lno) override {
        if (lno < base) return nullptr;
        size_t idx = lno - base;
        if (idx >= lines.size()) return nullptr;
        return &lines[idx];
    }
};

// One paired token of a line: a region under def:PairStart or def:PairEnd.
// Colorer makes those regions special (def:PairStart and def:PairEnd are
// children of def:Special), so they are not among the regions harvest()
// returns; FarColorer draws them only for the pair under the cursor. The
// fields are those of WasmRegion, with opens in place of the name.
struct WasmPair {
    int start;
    int end;
    int opens;  // 1 for def:PairStart, 0 for def:PairEnd
    unsigned int fore;
    unsigned int back;
    unsigned int style;
    int isForeSet;
    int isBackSet;
};

// One region of a line that Colorer's Outliner collects: under def:Outlined
// (FarColorer's list of functions) or def:Error (its list of errors). As in
// Outliner::addRegion, the first such region of a line starts an item and
// later ones on the same line add their text to it; starts tells which.
// Offsets, not text, cross into Go: labels are cut from the line there, since
// the library's legacy strings would re-encode them as CP1251.
struct WasmOutlineSpan {
    int error;   // 1 for def:Error, 0 for def:Outlined
    int start;
    int end;
    int level;   // Outliner's curLevel: schemes entered, counted from startParsing
    int starts;  // 1 when this region starts an item
    const char* name;
};

class WasmRegionHandler : public LineRegionsSupport {
public:
    std::vector<WasmRegion> regions;
    std::vector<WasmPair> pairs;
    std::vector<WasmOutlineSpan> outline;
    const Region* outlined = nullptr;
    const Region* error = nullptr;
    int level = 0;
    bool line_has_item[2] = {false, false};
    // Resolved by resolveRegions before each parse, until they exist.
    const Region* pair_start = nullptr;
    const Region* pair_end = nullptr;
    HrcLibrary* library = nullptr;
    std::unordered_map<const Region*, std::string>& name_cache;
    WasmLineSource* line_source = nullptr;

    WasmRegionHandler(std::unordered_map<const Region*, std::string>& cache)
        : name_cache(cache) {}

    void clear() {
        regions.clear();
        pairs.clear();
        outline.clear();
        LineRegionsSupport::clear();
    }

    // Looks up the def regions pairs and outlines are recognised by, once they
    // exist: def.hrc is loaded with the first type that imports it. It must run
    // outside TextParser::parse: parse holds the library's shared lock and
    // HrcLibrary::getRegion takes the exclusive one. Looked up from inside
    // addRegion, it trapped the module with no message.
    void resolveRegions() {
        if (!library || (pair_start && pair_end && outlined && error)) return;
        UnicodeString start_name("def:PairStart");
        UnicodeString end_name("def:PairEnd");
        UnicodeString outlined_name("def:Outlined");
        UnicodeString error_name("def:Error");
        pair_start = library->getRegion(&start_name);
        pair_end = library->getRegion(&end_name);
        outlined = library->getRegion(&outlined_name);
        error = library->getRegion(&error_name);
    }

    // Outliner's bookkeeping, beside LineRegionsSupport's own.
    void startParsing(size_t lno) override {
        LineRegionsSupport::startParsing(lno);
        level = 0;
    }
    void clearLine(size_t lno, UnicodeString* line) override {
        LineRegionsSupport::clearLine(lno, line);
        line_has_item[0] = line_has_item[1] = false;
    }
    void enterScheme(size_t lno, UnicodeString* line, int sx, int ex, const Region* region, const Scheme* scheme) override {
        LineRegionsSupport::enterScheme(lno, line, sx, ex, region, scheme);
        level++;
    }
    void leaveScheme(size_t lno, UnicodeString* line, int sx, int ex, const Region* region, const Scheme* scheme) override {
        LineRegionsSupport::leaveScheme(lno, line, sx, ex, region, scheme);
        level--;
    }
    void addRegion(size_t lno, UnicodeString* line, int sx, int ex, const Region* region) override {
        LineRegionsSupport::addRegion(lno, line, sx, ex, region);
        if (!region) return;
        for (int kind = 0; kind < 2; kind++) {
            const Region* search = kind == 0 ? outlined : error;
            if (!search || !region->hasParent(search)) continue;
            if (name_cache.find(region) == name_cache.end()) {
                name_cache[region] = UStr::to_stdstr(&region->getName());
            }
            outline.push_back({kind, sx, ex, level, line_has_item[kind] ? 0 : 1, name_cache[region].c_str()});
            line_has_item[kind] = true;
        }
    }

    // Whether region is a pair start (1), a pair end (0), or neither (-1),
    // tested as BaseEditor::getPairMatch and searchPair test it.
    int pairKind(const Region* region) {
        if (pair_start && region->hasParent(pair_start)) return 1;
        if (pair_end && region->hasParent(pair_end)) return 0;
        return -1;
    }

    static void styleOf(const LineRegion* lr, unsigned int& fore, unsigned int& back, unsigned int& style,
                        int& isForeSet, int& isBackSet) {
        fore = back = style = 0;
        isForeSet = isBackSet = 0;
        if (!lr->rdef) return;
        const StyledRegion* sr = StyledRegion::cast(lr->rdef);
        if (!sr) return;
        fore = sr->fore;
        back = sr->back;
        style = sr->style;
        isForeSet = sr->isForeSet ? 1 : 0;
        isBackSet = sr->isBackSet ? 1 : 0;
    }

    void harvest(size_t lno) {
        regions.clear();
        pairs.clear();
        for (LineRegion* lr = getLineRegions(lno); lr != nullptr; lr = lr->next) {
            if (lr->special) {
                if (lr->region != nullptr) {
                    int kind = pairKind(lr->region);
                    if (kind >= 0) {
                        WasmPair pair{lr->start, lr->end, kind, 0, 0, 0, 0, 0};
                        styleOf(lr, pair.fore, pair.back, pair.style, pair.isForeSet, pair.isBackSet);
                        pairs.push_back(pair);
                    }
                }
                continue;
            }
            if (lr->region == nullptr && lr->rdef == nullptr) {
                continue;
            }
            
            const char* name = "";
            if (lr->region != nullptr) {
                if (name_cache.find(lr->region) == name_cache.end()) {
                    name_cache[lr->region] = UStr::to_stdstr(&lr->region->getName());
                }
                name = name_cache[lr->region].c_str();
            }
            int end_idx = lr->end;
            
            unsigned int fore = 0, back = 0, style = 0;
            int isForeSet = 0, isBackSet = 0;
            if (lr->rdef) {
                const StyledRegion* sr = StyledRegion::cast(lr->rdef);
                if (sr) {
                    fore = sr->fore;
                    back = sr->back;
                    style = sr->style;
                    isForeSet = sr->isForeSet ? 1 : 0;
                    isBackSet = sr->isBackSet ? 1 : 0;
                }
            }

            regions.push_back({
                lr->start, end_idx, name,
                fore, back, style, isForeSet, isBackSet
            });
        }
    }
};

struct ColorerSession {
    std::unique_ptr<ParserFactory> factory;
    std::unique_ptr<TextParser> parser;
    WasmLineSource line_source;
    std::unordered_map<const Region*, std::string> name_cache;
    WasmRegionHandler region_handler;
    std::unique_ptr<RegionMapper> mapper;

    std::vector<const HrdNode*> hrd_cache;
    std::string last_hrd_name;
    std::string last_hrd_desc;

    // Filled by colorer_enum_file_types, read by the getters by index, the way
    // hrd_cache is. last_type_str holds the string the last getter returned.
    std::vector<FileType*> type_cache;
    std::string last_type_str;

    // The type the parser was given last, by colorer_select_type or
    // colorer_set_file_type; TextParser does not say.
    FileType* current_type = nullptr;
    std::string current_type_name;
    std::string last_param;

    // Reused across colorer_parse_line calls so a session that has already
    // seen a line at least this long does not pay for a malloc/free pair on
    // every subsequent one. Only grows: parse_line copies the bytes into a
    // UnicodeString before returning, so nothing needs to survive past that
    // call and there is nothing to shrink for.
    std::vector<char> line_buffer;

    ColorerSession() : region_handler(name_cache) {}
};

extern "C" {

void* colorer_alloc(size_t size) {
    return malloc(size);
}

void colorer_free(void* ptr) {
    free(ptr);
}

// Returns a pointer into the session's own line buffer, grown to at least
// min_size bytes if it wasn't already that big. The caller writes the line's
// UTF-8 bytes there and passes the same pointer straight to
// colorer_parse_line — this is what colorer_alloc/colorer_free used to be
// called for on every single line, and it needed neither: the buffer is
// reused, not owned per call. Returns nullptr for a bad handle or a
// negative size.
char* colorer_line_buffer(void* handle, int min_size) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || min_size < 0) return nullptr;
    if (static_cast<size_t>(min_size) > session->line_buffer.size()) {
        session->line_buffer.resize(static_cast<size_t>(min_size));
    }
    return session->line_buffer.data();
}

// Sets the most verbose Logger level delivered to the host, as Colorer's
// Logger::LogLevel (0 = off ... 5 = trace). Colorer's logger is a global, so
// this is per module instance, not per session; call it before colorer_init to
// see what loading the catalog reports.
void colorer_set_log_level(int level) {
    if (level <= Logger::LOG_OFF) {
        host_logger.level = Logger::LOG_OFF;
        Log::removeLogger();
        return;
    }
    if (level > Logger::LOG_TRACE) {
        level = Logger::LOG_TRACE;
    }
    host_logger.level = static_cast<Logger::LogLevel>(level);
    Log::registerLogger(host_logger);
}

void* colorer_init(const char* catalog_path) {
    ColorerSession* session = new ColorerSession();
    session->region_handler.line_source = &session->line_source;
    session->region_handler.resize(1);
    session->factory = std::make_unique<ParserFactory>();
    UnicodeString cat(catalog_path);
    session->factory->loadCatalog(&cat);
    session->region_handler.library = &session->factory->getHrcLibrary();
    session->parser = session->factory->createTextParser();
    session->parser->setLineSource(&session->line_source);
    session->parser->setRegionHandler(&session->region_handler);
    return session;
}

// The user's own colour styles, loaded on top of the catalog: an XML file in
// the catalog's <hrd-sets> format, or a folder of .hrd files each naming its
// class and name. This is FarColorer's "user file of color styles", and like
// FarColorer (FarHrcSettings::applySettings) it is loaded after the catalog and
// before the user's schemes. Returns 0 for a bad handle.
int colorer_load_user_hrd(void* handle, const char* path) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !path) return 0;
    UnicodeString location(path);
    session->factory->loadHrdPath(&location);
    return 1;
}

// The user's own schemes: an .hrc file, or a folder whose .hrc files are all
// loaded except *.ent.hrc — FarColorer's "user file of schemes". Returns 0 for
// a bad handle.
int colorer_load_user_hrc(void* handle, const char* path) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !path) return 0;
    UnicodeString location(path);
    session->factory->loadHrcPath(&location);
    return 1;
}

// HRC settings, as FarColorer's readSystemHrcSettings loads
// plug/hrcsettings.xml: an <hrc-settings> file whose prototypes give file types
// parameters and their defaults — hotkey, favorite, show-cross and the rest.
// FarColorer loads them after the catalog and before the user's styles.
// Returns 0 for a bad handle.
int colorer_load_hrc_settings(void* handle, const char* path) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !path) return 0;
    UnicodeString location(path);
    session->factory->loadHrcSettings(&location, false);
    return 1;
}

void colorer_destroy(void* handle) {
    if (handle) {
        delete static_cast<ColorerSession*>(handle);
    }
}

void colorer_reset_session(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (session) {
        session->line_source.clear();
        session->parser->clearCache();
        session->region_handler.clear();
    }
}

int colorer_set_hrd(void* handle, const char* hrd_class, const char* hrd_name) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    UnicodeString cls(hrd_class);
    UnicodeString name(hrd_name);
    try {
        session->mapper = session->factory->createStyledMapper(&cls, &name);
        session->region_handler.setRegionMapper(session->mapper.get());
        UnicodeString def_text("def:Text");
        session->region_handler.setBackground(session->mapper->getRegionDefine(def_text));
        UnicodeString def_spec("def:Special");
        session->region_handler.setSpecialRegion(session->factory->getHrcLibrary().getRegion(&def_spec));
        return 1;
    } catch (...) {
        return 0;
    }
}

int colorer_enum_hrd_instances(void* handle, const char* class_id) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    UnicodeString cls(class_id);
    session->hrd_cache = session->factory->enumHrdInstances(cls);
    return session->hrd_cache.size();
}

const char* colorer_get_hrd_name(void* handle, int index) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || index < 0 || index >= session->hrd_cache.size()) return nullptr;
    session->last_hrd_name = UStr::to_stdstr(&session->hrd_cache[index]->hrd_name);
    return session->last_hrd_name.c_str();
}

const char* colorer_get_hrd_description(void* handle, int index) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || index < 0 || index >= session->hrd_cache.size()) return nullptr;
    session->last_hrd_desc = UStr::to_stdstr(&session->hrd_cache[index]->hrd_description);
    return session->last_hrd_desc.c_str();
}

int colorer_get_region_define(void* handle, const char* name, unsigned int* fore, unsigned int* back, unsigned int* style, int* isForeSet, int* isBackSet) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !session->mapper) return 0;
    UnicodeString reg_name(name);
    const RegionDefine* rd = session->mapper->getRegionDefine(reg_name);
    if (rd) {
        const StyledRegion* sr = StyledRegion::cast(rd);
        if (sr) {
            if (fore) *fore = sr->fore;
            if (back) *back = sr->back;
            if (style) *style = sr->style;
            if (isForeSet) *isForeSet = sr->isForeSet ? 1 : 0;
            if (isBackSet) *isBackSet = sr->isBackSet ? 1 : 0;
            return 1;
        }
    }
    return 0;
}

int colorer_select_type(void* handle, const char* file_name, const char* first_line) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    UnicodeString fname(file_name);
    UnicodeString fline(first_line);
    FileType* type = session->factory->getHrcLibrary().chooseFileType(&fname, &fline);
    if (!type) return 0;
    session->factory->getHrcLibrary().loadFileType(type);
    session->parser->setFileType(type);
    session->current_type = type;
    return 1;
}

static UnicodeString utf8(const char* s) {
    return UnicodeString(s, static_cast<int32_t>(strlen(s)), Encodings::ENC_UTF8);
}

// Gives the parser the named type instead of choosing one by file name, as
// FarColorer's list of types does. Returns 0 when no type has the name.
int colorer_set_file_type(void* handle, const char* name) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !name) return 0;
    UnicodeString type_name = utf8(name);
    auto& library = session->factory->getHrcLibrary();
    FileType* type = library.getFileType(&type_name);
    if (!type) return 0;
    library.loadFileType(type);
    session->parser->setFileType(type);
    session->current_type = type;
    return 1;
}

// The name of the type the parser has, or "" before one is set.
const char* colorer_file_type(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return nullptr;
    session->current_type_name.clear();
    if (session->current_type) {
        session->current_type_name = session->current_type->getName().getChars(Encodings::ENC_UTF8);
    }
    return session->current_type_name.c_str();
}

// A parameter's value for a type, the user's value if there is one, as UTF-8;
// nullptr when the type or the parameter does not exist.
const char* colorer_get_file_type_param(void* handle, const char* type_name, const char* param) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !type_name || !param) return nullptr;
    UnicodeString name = utf8(type_name);
    FileType* type = session->factory->getHrcLibrary().getFileType(&name);
    if (!type) return nullptr;
    const UnicodeString* value = type->getParamValue(utf8(param));
    if (!value) return nullptr;
    session->last_param = value->getChars(Encodings::ENC_UTF8);
    return session->last_param.c_str();
}

// FarEditorSet::addParamAndValue: sets the user's value of a type's parameter,
// first adding the parameter with the "default" type's value when the type
// lacks it. Returns 1, 0 when the type does not exist, -1 when neither the type
// nor "default" has the parameter.
int colorer_set_file_type_param(void* handle, const char* type_name, const char* param, const char* value) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !type_name || !param || !value) return 0;
    auto& library = session->factory->getHrcLibrary();
    UnicodeString name = utf8(type_name);
    FileType* type = library.getFileType(&name);
    if (!type) return 0;
    UnicodeString param_name = utf8(param);
    if (type->getParamValue(param_name) == nullptr) {
        UnicodeString default_name("default");
        FileType* def = library.getFileType(&default_name);
        const UnicodeString* default_value = def ? def->getParamValue(param_name) : nullptr;
        if (!default_value) return -1;
        type->addParam(param_name, *default_value);
    }
    UnicodeString param_value = utf8(value);
    type->setParamValue(param_name, &param_value);
    return 1;
}

// Lists the file types the catalog and the user's schemes declare, in the
// library's order, for the getters below. Returns the count.
int colorer_enum_file_types(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    session->type_cache.clear();
    auto& library = session->factory->getHrcLibrary();
    for (unsigned int idx = 0;; idx++) {
        FileType* type = library.enumerateFileTypes(idx);
        if (!type) break;
        session->type_cache.push_back(type);
    }
    return static_cast<int>(session->type_cache.size());
}

static const char* file_type_string(void* handle, int index, const UnicodeString& (FileType::*field)() const) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || index < 0 || static_cast<size_t>(index) >= session->type_cache.size()) return nullptr;
    session->last_type_str = UStr::to_stdstr(&(session->type_cache[index]->*field)());
    return session->last_type_str.c_str();
}

const char* colorer_get_file_type_name(void* handle, int index) {
    return file_type_string(handle, index, &FileType::getName);
}

const char* colorer_get_file_type_group(void* handle, int index) {
    return file_type_string(handle, index, &FileType::getGroup);
}

const char* colorer_get_file_type_description(void* handle, int index) {
    return file_type_string(handle, index, &FileType::getDescription);
}

// Loads the scheme of the named file type — what colorer_select_type does for
// the type it picks — without selecting it. Returns 1 when the type now has a
// base scheme, 0 when it loaded without one, -1 when no type has that name.
// A scheme Colorer cannot parse aborts the call, as it does on selection.
int colorer_load_file_type(void* handle, const char* name) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || !name) return -1;
    UnicodeString type_name(name);
    auto& library = session->factory->getHrcLibrary();
    FileType* type = library.getFileType(&type_name);
    if (!type) return -1;
    library.loadFileType(type);
    return type->getBaseScheme() != nullptr ? 1 : 0;
}

int colorer_parse_line(void* handle, const char* line_utf8, int line_len) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return -1;
    session->region_handler.clear();

    size_t lno = session->line_source.nextLine();
    session->line_source.append(UnicodeString(line_utf8, line_len, Encodings::ENC_UTF8));

    session->region_handler.setFirstLine(lno);
    session->region_handler.resolveRegions();
    session->parser->parse(lno, 1, TextParser::TextParseMode::TPM_CACHE_UPDATE);
    session->region_handler.harvest(lno);
    return session->region_handler.regions.size();
}

// Every field of WasmRegion is 4 bytes on wasm32 (int, unsigned int, and a
// pointer alike), so the struct is 32 bytes with no padding — this is what
// lets colorer_get_regions hand the whole array back as one flat read
// instead of eight host calls per region. If that ever stops holding, this
// assertion fails the build instead of silently misreading memory.
static_assert(sizeof(WasmRegion) == 32, "WasmRegion must pack to 32 bytes for the batched Go reader");

// Returns a pointer to session->region_handler.regions.data(): count() *
// sizeof(WasmRegion) bytes, valid until the next colorer_parse_line or
// colorer_reset_session call on this handle. The caller already knows the
// count from colorer_parse_line's return value.
const void* colorer_get_regions(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return nullptr;
    return session->region_handler.regions.data();
}

static_assert(sizeof(WasmPair) == 32, "WasmPair must pack to 32 bytes for the batched Go reader");

static_assert(sizeof(WasmOutlineSpan) == 24, "WasmOutlineSpan must pack to 24 bytes for the batched Go reader");

// The outline regions of the line colorer_parse_line parsed last, in the order
// Colorer reported them; valid until the next parse or reset.
int colorer_outline_count(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    return static_cast<int>(session->region_handler.outline.size());
}

const void* colorer_get_outline(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return nullptr;
    return session->region_handler.outline.data();
}

// The pairs of the line colorer_parse_line parsed last, in the order Colorer
// reported them; valid until the next parse or reset.
int colorer_pair_count(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return 0;
    return static_cast<int>(session->region_handler.pairs.size());
}

const void* colorer_get_pairs(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return nullptr;
    return session->region_handler.pairs.data();
}

void colorer_forget_before(void* handle, int lno) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session || lno <= 0) return;
    session->line_source.forgetBefore(static_cast<size_t>(lno));
}

int colorer_first_line(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return -1;
    return static_cast<int>(session->line_source.firstLine());
}

int colorer_next_line(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return -1;
    return static_cast<int>(session->line_source.nextLine());
}

int colorer_get_region_start(void* handle, int index) {
    auto* session = static_cast<ColorerSession*>(handle);
    return session->region_handler.regions[index].start;
}

int colorer_get_region_end(void* handle, int index) {
    auto* session = static_cast<ColorerSession*>(handle);
    return session->region_handler.regions[index].end;
}

const char* colorer_get_region_name(void* handle, int index) {
    auto* session = static_cast<ColorerSession*>(handle);
    return session->region_handler.regions[index].name;
}

unsigned int colorer_get_region_fore(void* handle, int index) {
    return static_cast<ColorerSession*>(handle)->region_handler.regions[index].fore;
}
unsigned int colorer_get_region_back(void* handle, int index) {
    return static_cast<ColorerSession*>(handle)->region_handler.regions[index].back;
}
unsigned int colorer_get_region_style(void* handle, int index) {
    return static_cast<ColorerSession*>(handle)->region_handler.regions[index].style;
}
int colorer_get_region_is_fore_set(void* handle, int index) {
    return static_cast<ColorerSession*>(handle)->region_handler.regions[index].isForeSet;
}
int colorer_get_region_is_back_set(void* handle, int index) {
    return static_cast<ColorerSession*>(handle)->region_handler.regions[index].isBackSet;
}

}