#pragma once
#include "protocol_generated.h"

void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands);
