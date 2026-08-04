#include "colorer_wrapper.h"
#include <colorer/ParserFactory.h>
#include <colorer/TextParser.h>
#include <colorer/LineSource.h>
#include <colorer/handlers/LineRegionsSupport.h>
#include <vector>
#include <string>
#include <unordered_map>

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

class WasmLineSource : public LineSource {
public:
    std::vector<UnicodeString> lines;
    UnicodeString* getLine(size_t lno) override {
        if (lno >= lines.size()) return nullptr;
        return &lines[lno];
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

    ColorerSession() : region_handler(name_cache) {}
};

extern "C" {

void* colorer_alloc(size_t size) {
    return malloc(size);
}

void colorer_free(void* ptr) {
    free(ptr);
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

void colorer_destroy(void* handle) {
    if (handle) {
        delete static_cast<ColorerSession*>(handle);
    }
}

void colorer_reset_session(void* handle) {
    auto* session = static_cast<ColorerSession*>(handle);
    if (session) {
        session->line_source.lines.clear();
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

    size_t lno = session->line_source.lines.size();
    session->line_source.lines.push_back(UnicodeString(line_utf8, line_len, Encodings::ENC_UTF8));

    session->region_handler.setFirstLine(lno);
    session->parser->parse(lno, 1, TextParser::TextParseMode::TPM_CACHE_UPDATE);
    session->region_handler.harvest(lno);
    return session->region_handler.regions.size();
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