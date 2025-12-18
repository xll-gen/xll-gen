#include "include/xll_ipc.h"
#include "include/protocol_generated.h" // Ensure we include this to use protocol:: namespace if needed in IPC
#include <random>
#include <thread>
#include <iostream>

// Global definitions
shm::DirectHost g_host;
std::map<std::string, bool> g_sentRefCache;
std::mutex g_refCacheMutex;
