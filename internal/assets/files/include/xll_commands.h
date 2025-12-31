#pragma once
#include "types/protocol_generated.h"
#include <flatbuffers/flatbuffers.h>


void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands);
