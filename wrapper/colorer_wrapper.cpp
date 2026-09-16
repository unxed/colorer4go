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

class WasmRegionHandler : public LineRegionsSupport {
public:
    std::vector<WasmRegion> regions;
    std::unordered_map<const Region*, std::string>& name_cache;
    WasmLineSource* line_source = nullptr;

    WasmRegionHandler(std::unordered_map<const Region*, std::string>& cache)
        : name_cache(cache) {}

    void clear() {
        regions.clear();
        LineRegionsSupport::clear();
    }

    void harvest(size_t lno) {
        regions.clear();
        for (LineRegion* lr = getLineRegions(lno); lr != nullptr; lr = lr->next) {
            if (lr->special) {
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
    return 1;
}

int colorer_parse_line(void* handle, const char* line_utf8, int line_len) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (!session) return -1;
    session->region_handler.clear();

    size_t lno = session->line_source.nextLine();
    session->line_source.append(UnicodeString(line_utf8, line_len, Encodings::ENC_UTF8));

    session->region_handler.setFirstLine(lno);
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