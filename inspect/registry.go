package inspect

type Registry struct {
	inspectors []Inspector
}

func NewRegistry() *Registry {
	r := &Registry{}
	r.add(&cephHealth{}, &cephOSD{}, &cephMon{}, &cephPG{}, &cephCapacity{}, &cephSlowOps{}, &cephCrash{})
	r.add(&hwCPU{}, &hwMemory{}, &hwDiskSmart{}, &hwDiskUsage{}, &hwNIC{}, &hwBond{}, &hwPCIeLink{})
	return r
}

func (r *Registry) add(in ...Inspector) { r.inspectors = append(r.inspectors, in...) }

func (r *Registry) ByScope(s Scope) []Inspector {
	var out []Inspector
	for _, in := range r.inspectors {
		if in.Scope() == s {
			out = append(out, in)
		}
	}
	return out
}

func (r *Registry) All() []Inspector { return r.inspectors }
