// Export numeric character evidence from the same pinned PDF engine as the XML.
#include <Error.h>
#include <GlobalParams.h>
#include <PDFDoc.h>
#include <TextOutputDev.h>
#include <goo/GooString.h>
#include <cmath>
#include <iomanip>
#include <iostream>
#include <memory>
#include <sys/resource.h>

int main(int argc, char **argv)
{
    if (argc != 2) return 1;
    rlimit memory{512UL << 20, 512UL << 20}, cpu{30, 30};
    if (setrlimit(RLIMIT_AS, &memory) || setrlimit(RLIMIT_CPU, &cpu)) return 1;
    setErrorCallback([](ErrorCategory, Goffset, const char *) {});
    globalParams = std::make_unique<GlobalParams>();
    PDFDoc pdf(std::make_unique<GooString>(argv[1]));
    if (!pdf.isOk() || pdf.isEncrypted() || pdf.getNumPages() < 1 || pdf.getNumPages() > 40) return 1;
    std::cout << std::setprecision(12) << "{\"pages\":[";
    int count = 0;
    for (int page = 1; page <= pdf.getNumPages(); ++page) {
        const double width = pdf.getPageMediaWidth(page), height = pdf.getPageMediaHeight(page);
        if (!std::isfinite(width) || !std::isfinite(height) || width <= 0 || height <= 0 || width > 2000 || height > 2000) return 1;
        TextOutputDev output(nullptr, false, 0, true, false);
        pdf.displayPage(&output, page, 72, 72, 0, true, false, false);
        auto words = output.makeWordList();
        if (!words || words->getLength() > 65536) return 1;
        if (page > 1) std::cout << ',';
        std::cout << "{\"page\":" << page << ",\"width\":" << width << ",\"height\":" << height << ",\"glyphs\":[";
        bool first = true;
        for (int wordIndex = 0; wordIndex < words->getLength(); ++wordIndex) {
            const auto *word = words->get(wordIndex);
            if (word->getLength() > 8192) return 1;
            for (int index = 0; index < word->getLength(); ++index) {
                const unsigned scalar = *word->getChar(index);
                if (++count > 250000 || scalar > 0x10ffff || (scalar >= 0xd800 && scalar <= 0xdfff)) return 1;
                double left, top, right, bottom;
                word->getCharBBox(index, &left, &top, &right, &bottom);
                // Unusable edge/rotated evidence stays unavailable, never guessed.
                if (word->getRotation() || scalar <= 32 || !std::isfinite(left) || !std::isfinite(top) || !std::isfinite(right) || !std::isfinite(bottom) || left < 0 || top < 0 || right > width || bottom > height || right <= left || bottom <= top) continue;
                if (!first) std::cout << ',';
                first = false;
                std::cout << "{\"text\":\"";
                const auto escaped = [](unsigned value) { std::cout << "\\u" << std::hex << std::setw(4) << std::setfill('0') << value << std::dec; };
                if (scalar <= 0xffff) escaped(scalar);
                else { escaped(0xd800 + ((scalar - 0x10000) >> 10)); escaped(0xdc00 + ((scalar - 0x10000) & 1023)); }
                std::cout << "\",\"box\":{\"x\":" << left / width << ",\"y\":" << top / height << ",\"width\":" << (right - left) / width << ",\"height\":" << (bottom - top) / height << "}}";
            }
        }
        std::cout << "]}";
    }
    std::cout << "]}";
    return std::cout.good() ? 0 : 1;
}
