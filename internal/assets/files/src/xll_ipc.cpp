#include "xll_ipc.h"
#include "protocol_generated.h"
#include <random>
#include <thread>
#include <iostream>

// Global definitions
shm::DirectHost* g_phost = nullptr;
std::map<std::string, bool> g_sentRefCache;
std::mutex g_refCacheMutex;

// Helper for logging SHM errors
std::string SHMErrorToString(shm::Error err) {
    switch (err) {
        case shm::Error::None: return "None";
        case shm::Error::Timeout: return "Timeout";
        case shm::Error::BufferTooSmall: return "BufferTooSmall";
        case shm::Error::InvalidArgs: return "InvalidArgs";
        case shm::Error::NotConnected: return "NotConnected";
        case shm::Error::ResourceExhausted: return "ResourceExhausted";
        case shm::Error::ProtocolViolation: return "ProtocolViolation";
        default: return "Unknown (" + std::to_string((int)err) + ")";
    }
}
