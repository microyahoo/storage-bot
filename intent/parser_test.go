package intent

import "testing"

func TestParseSkillWithAll(t *testing.T) {
	clusters := []string{"cluster-01", "cluster-02"}

	cases := []struct {
		msg         string
		wantSkill   string
		wantCluster string
	}{
		{"set_no_backfill all", "set_no_backfill", "all"},
		{"set nobackfill all", "set_no_backfill", "all"},
		{"set nobackfill 所有", "set_no_backfill", "all"},
		{"set nobackfill 全部", "set_no_backfill", "all"},
		{"unset nobackfill all", "unset_no_backfill", "all"},
		{"set noout all", "set_noout", "all"},
		{"unset noout all", "unset_noout", "all"},
		{"set nobackfill cluster-01", "set_no_backfill", "cluster-01"},
		{"list node cluster-01", "list_nodes", "cluster-01"},
		{"list_nodes cluster-02", "list_nodes", "cluster-02"},
	}

	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			action := ParseWithAll(c.msg, clusters, nil, nil)
			if action.Type != ActionSkill {
				t.Errorf("msg=%q: got type=%v, want ActionSkill", c.msg, action.Type)
				return
			}
			if action.SkillName != c.wantSkill {
				t.Errorf("msg=%q: got skill=%q, want %q", c.msg, action.SkillName, c.wantSkill)
			}
			if action.ClusterName != c.wantCluster {
				t.Errorf("msg=%q: got cluster=%q, want %q", c.msg, action.ClusterName, c.wantCluster)
			}
		})
	}
}

func TestParseListNodeNotNodeDiag(t *testing.T) {
	clusters := []string{"cdn", "cluster-01"}

	cases := []struct {
		msg         string
		wantSkill   string
		wantCluster string
	}{
		{"list_node cdn", "list_nodes", "cdn"},
		{"list node cdn", "list_nodes", "cdn"},
		{"cdn 节点列表", "list_nodes", "cdn"},
	}

	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			action := ParseWithAll(c.msg, clusters, nil, nil)
			if action.Type != ActionSkill {
				t.Errorf("msg=%q: got type=%v (%s), want ActionSkill", c.msg, action.Type, action.Type)
				return
			}
			if action.SkillName != c.wantSkill {
				t.Errorf("msg=%q: skill=%q, want %q", c.msg, action.SkillName, c.wantSkill)
			}
			if action.ClusterName != c.wantCluster {
				t.Errorf("msg=%q: cluster=%q, want %q", c.msg, action.ClusterName, c.wantCluster)
			}
		})
	}
}
