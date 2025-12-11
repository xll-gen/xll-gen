#include <fstream>
#include <vector>
#include <iostream>
#include <zstd.h>

int main(int argc, char* argv[]) {
    if (argc < 3) {
        std::cerr << "Usage: compressor <input> <output>" << std::endl;
        return 1;
    }

    std::ifstream inFile(argv[1], std::ios::binary | std::ios::ate);
    if (!inFile) {
        std::cerr << "Failed to open input file: " << argv[1] << std::endl;
        return 1;
    }
    size_t srcSize = (size_t)inFile.tellg();
    inFile.seekg(0, std::ios::beg);

    std::vector<char> srcBuffer(srcSize);
    if (!inFile.read(srcBuffer.data(), srcSize)) {
        std::cerr << "Failed to read input file." << std::endl;
        return 1;
    }

    size_t dstCapacity = ZSTD_compressBound(srcSize);
    std::vector<char> dstBuffer(dstCapacity);

    // Compression Level 22 (Ultra)
    size_t compressedSize = ZSTD_compress(dstBuffer.data(), dstCapacity,
                                          srcBuffer.data(), srcSize,
                                          22);

    if (ZSTD_isError(compressedSize)) {
        std::cerr << "Zstd Error: " << ZSTD_getErrorName(compressedSize) << std::endl;
        return 1;
    }

    std::ofstream outFile(argv[2], std::ios::binary);
    if (!outFile) {
        std::cerr << "Failed to open output file: " << argv[2] << std::endl;
        return 1;
    }
    outFile.write(dstBuffer.data(), compressedSize);

    std::cout << "Compressed " << srcSize << " bytes to " << compressedSize << " bytes." << std::endl;
    return 0;
}
