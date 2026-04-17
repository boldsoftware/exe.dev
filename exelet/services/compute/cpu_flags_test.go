package compute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCPUFlags(t *testing.T) {
	cpuinfo := `processor	: 0
vendor_id	: GenuineIntel
cpu family	: 6
model		: 85
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush mmx fxsr sse sse2 ss ht syscall nx pdpe1gb rdtscp lm constant_tsc rep_good nopl xtopology cpuid pni pclmulqdq ssse3 fma cx16 pcid sse4_1 sse4_2 x2apic movbe popcnt tsc_deadline_timer aes xsave avx f16c rdrand hypervisor lahf_lm abm 3dnowprefetch cpuid_fault ssbd ibrs ibpb stibp ibrs_enhanced fsgsbase tsc_adjust bmi1 avx2 smep bmi2 erms invpcid avx512f avx512dq rdseed adx smap clflushopt clwb avx512cd avx512bw avx512vl xsaveopt xsavec xgetbv1 xsaves
bugs		: spectre_v1 spectre_v2 spec_store_bypass

processor	: 1
vendor_id	: GenuineIntel
cpu family	: 6
model		: 85
flags		: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush mmx fxsr sse sse2 ss ht syscall nx pdpe1gb rdtscp lm constant_tsc rep_good nopl xtopology cpuid pni pclmulqdq ssse3 fma cx16 pcid sse4_1 sse4_2 x2apic movbe popcnt tsc_deadline_timer aes xsave avx f16c rdrand hypervisor lahf_lm abm 3dnowprefetch cpuid_fault ssbd ibrs ibpb stibp ibrs_enhanced fsgsbase tsc_adjust bmi1 avx2 smep bmi2 erms invpcid avx512f avx512dq rdseed adx smap clflushopt clwb avx512cd avx512bw avx512vl xsaveopt xsavec xgetbv1 xsaves
bugs		: spectre_v1 spectre_v2 spec_store_bypass
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cpuinfo")
	if err := os.WriteFile(path, []byte(cpuinfo), 0o644); err != nil {
		t.Fatal(err)
	}

	flags := parseCPUFlags(path)
	if len(flags) == 0 {
		t.Fatal("expected non-empty flags")
	}

	// Check a few known flags are present.
	flagSet := make(map[string]bool, len(flags))
	for _, f := range flags {
		flagSet[f] = true
	}
	for _, want := range []string{"avx2", "avx512f", "sse4_2", "fpu"} {
		if !flagSet[want] {
			t.Errorf("missing expected flag %q", want)
		}
	}

	// Verify sorted order.
	for i := 1; i < len(flags); i++ {
		if flags[i] < flags[i-1] {
			t.Errorf("flags not sorted: %q before %q", flags[i-1], flags[i])
		}
	}
}

func TestParseCPUFlagsNoFile(t *testing.T) {
	flags := parseCPUFlags("/nonexistent/cpuinfo")
	if flags != nil {
		t.Errorf("expected nil for missing file, got %v", flags)
	}
}

func TestParseCPUFlagsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cpuinfo")
	if err := os.WriteFile(path, []byte("processor : 0\nmodel : 85\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := parseCPUFlags(path)
	if len(flags) != 0 {
		t.Errorf("expected empty flags for cpuinfo without flags line, got %v", flags)
	}
}
