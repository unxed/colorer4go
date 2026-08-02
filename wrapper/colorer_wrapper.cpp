#include "colorer_wrapper.h"
#include <colorer/ParserFactory.h>

extern "C" {

void* colorer_factory_create() {
    return new ParserFactory();
}

void colorer_factory_destroy(void* pf) {
    if (pf) {
        delete static_cast<ParserFactory*>(pf);
    }
}

}