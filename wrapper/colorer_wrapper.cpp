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
            if (lr->special || lr->region == nullptr) {
                continue;
            }
            const Region* region = lr->region;
            if (name_cache.find(region) == name_cache.end()) {
                std::string chain = UStr::to_stdstr(&region->getName());
                const Region* p = region->getParent();
                while (p) {
                    chain += "|";
                    chain += UStr::to_stdstr(&p->getName());
                    p = p->getParent();
                }
                name_cache[region] = chain;
            }
            int end_idx = lr->end;
            if (end_idx == -1) {
                end_idx = line_source->lines[lno].length();
            }
            regions.push_back({lr->start, end_idx, name_cache[region].c_str()});
        }
    }
};

struct ColorerSession {
    std::unique_ptr<ParserFactory> factory;
    std::unique_ptr<TextParser> parser;
    WasmLineSource line_source;
    std::unordered_map<const Region*, std::string> name_cache;
    WasmRegionHandler region_handler;

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

}