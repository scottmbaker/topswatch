package npu

// pmtGUIDs maps CPU generation to their NPU telemetry PMT GUIDs.
var pmtGUIDs = map[CPUGen][]string{
	GenMeteorLake:  {"0x130670b2"},
	GenArrowLake:   {"0x1306a0b3", "0x1306a0b2", "0x1306a0b4"},
	GenLunarLake:   {"0x3072005"},
	GenPantherLake: {"0x3086000"},
}

// registerDef describes a field within the PMT telemetry buffer.
type registerDef struct {
	offset  int // byte offset into the telemetry buffer
	bitLo   int // low bit (inclusive)
	bitHi   int // high bit (inclusive)
	size    int // read size in bytes (4 or 8)
}

// genRegisters holds the register layout for a CPU generation.
type genRegisters struct {
	energy      registerDef
	temperature registerDef
	workpoint   registerDef
	memoryBW    registerDef
}

var registerTable = map[CPUGen]genRegisters{
	GenMeteorLake: {
		energy:      registerDef{offset: 0x628, bitLo: 0, bitHi: 63, size: 8},
		temperature: registerDef{offset: 0x98, bitLo: 40, bitHi: 47, size: 8},
		workpoint:   registerDef{offset: 0x68, bitLo: 0, bitHi: 23, size: 4},
		memoryBW:    registerDef{offset: 0x0, bitLo: 0, bitHi: 31, size: 4},
	},
	GenArrowLake: {
		energy:      registerDef{offset: 0x628, bitLo: 0, bitHi: 63, size: 8},
		temperature: registerDef{offset: 0x98, bitLo: 40, bitHi: 47, size: 8},
		workpoint:   registerDef{offset: 0x68, bitLo: 0, bitHi: 23, size: 4},
		memoryBW:    registerDef{offset: 0x0, bitLo: 0, bitHi: 31, size: 4},
	},
	GenLunarLake: {
		energy:      registerDef{offset: 0x5d0, bitLo: 0, bitHi: 63, size: 8},
		temperature: registerDef{offset: 0x70, bitLo: 40, bitHi: 47, size: 8},
		workpoint:   registerDef{offset: 0x18, bitLo: 0, bitHi: 23, size: 4},
		memoryBW:    registerDef{offset: 0xc18, bitLo: 0, bitHi: 31, size: 4},
	},
	GenPantherLake: {
		energy:      registerDef{offset: 0x670, bitLo: 0, bitHi: 63, size: 8},
		temperature: registerDef{offset: 0x78, bitLo: 40, bitHi: 47, size: 8},
		workpoint:   registerDef{offset: 0x18, bitLo: 0, bitHi: 23, size: 4},
		memoryBW:    registerDef{offset: 0xc18, bitLo: 0, bitHi: 31, size: 4},
	},
}

// freqRawToMHz converts the raw frequency byte from VPU_WORKPOINT to MHz.
func freqRawToMHz(gen CPUGen, raw uint64) float64 {
	switch gen {
	case GenMeteorLake, GenArrowLake:
		// MTL & ARL: PLL ratio * 50 MHz * 2/3 = ratio * 100/3 MHz.
		// Source: kernel drivers/accel/ivpu/ivpu_hw_btrs.c pll_ratio_to_dpu_freq_mtl().
		return float64(raw) * 100.0 / 3.0
	default:
		// LNL, PTL: PLL ratio * 50 MHz / 2 = ratio * 25 MHz.
		// Source: kernel drivers/accel/ivpu/ivpu_hw_btrs.c pll_ratio_to_dpu_freq_lnl().
		return float64(raw) * 25.0
	}
}

// energyToJoules converts the raw energy counter (U32.18.14 fixed-point) to joules.
func energyToJoules(raw uint64) float64 {
	return float64(raw) / 16384.0 // 2^14 fractional bits
}
