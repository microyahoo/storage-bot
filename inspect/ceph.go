package inspect

type cephHealth struct{}

func (cephHealth) Name() string        { return "ceph_health" }
func (cephHealth) Description() string { return "Ceph 集群健康状态" }
func (cephHealth) Scope() Scope        { return ClusterScope }
func (cephHealth) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("health", "detail")
	if err != nil {
		return []Finding{{Item: "ceph_health", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCephHealth(raw)}, nil
}

type cephOSD struct{}

func (cephOSD) Name() string        { return "ceph_osd" }
func (cephOSD) Description() string { return "OSD up/in 状态" }
func (cephOSD) Scope() Scope        { return ClusterScope }
func (cephOSD) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("osd", "stat")
	if err != nil {
		return []Finding{{Item: "ceph_osd", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseOSDStat(raw)}, nil
}

type cephMon struct{}

func (cephMon) Name() string        { return "ceph_mon" }
func (cephMon) Description() string { return "Monitor quorum 状态" }
func (cephMon) Scope() Scope        { return ClusterScope }
func (cephMon) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("quorum_status")
	if err != nil {
		return []Finding{{Item: "ceph_mon", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseMonQuorum(raw)}, nil
}

type cephPG struct{}

func (cephPG) Name() string        { return "ceph_pg" }
func (cephPG) Description() string { return "PG 状态" }
func (cephPG) Scope() Scope        { return ClusterScope }
func (cephPG) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("pg", "stat")
	if err != nil {
		return []Finding{{Item: "ceph_pg", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parsePGStat(raw)}, nil
}

type cephCapacity struct{}

func (cephCapacity) Name() string        { return "ceph_capacity" }
func (cephCapacity) Description() string { return "集群容量使用率" }
func (cephCapacity) Scope() Scope        { return ClusterScope }
func (cephCapacity) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("df")
	if err != nil {
		return []Finding{{Item: "ceph_capacity", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCephDF(raw, ic.Thresholds.CapacityWarnPct, ic.Thresholds.CapacityCritPct)}, nil
}

type cephSlowOps struct{}

func (cephSlowOps) Name() string        { return "ceph_slow_ops" }
func (cephSlowOps) Description() string { return "慢请求" }
func (cephSlowOps) Scope() Scope        { return ClusterScope }
func (cephSlowOps) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("health", "detail")
	if err != nil {
		return []Finding{{Item: "ceph_slow_ops", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseSlowOps(raw)}, nil
}

type cephCrash struct{}

func (cephCrash) Name() string        { return "ceph_crash" }
func (cephCrash) Description() string { return "未确认 crash" }
func (cephCrash) Scope() Scope        { return ClusterScope }
func (cephCrash) Inspect(ic *InspectContext) ([]Finding, error) {
	raw, err := ic.RunCeph("crash", "ls-new")
	if err != nil {
		return []Finding{{Item: "ceph_crash", Level: LevelUnknown, Summary: "采集失败", Detail: err.Error()}}, nil
	}
	return []Finding{parseCrashLs(raw)}, nil
}
