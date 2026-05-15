package analyzer

import (
	"context"
	"fmt"
)

type Analyzer struct {
	provider LLMProvider
}

func NewAnalyzer(provider LLMProvider) *Analyzer {
	return &Analyzer{provider: provider}
}

func (a *Analyzer) Analyze(ctx context.Context, clusterName, diagnosticData string) (string, error) {
	systemPrompt := fmt.Sprintf(`You are a Ceph storage expert. Analyze the following diagnostic output from Ceph cluster "%s".
Identify any issues, determine root cause, and provide specific remediation steps.
Format your response in Chinese with these sections:
## 状态摘要
## 发现的问题
## 根因分析
## 修复建议

If the cluster is healthy, state that clearly and briefly. Be concise and actionable.`, clusterName)

	const maxDiagLen = 150000
	if len(diagnosticData) > maxDiagLen {
		diagnosticData = diagnosticData[:maxDiagLen] + "\n... [truncated]"
	}

	return a.provider.Chat(ctx, systemPrompt, diagnosticData)
}

func (a *Analyzer) Provider() LLMProvider {
	return a.provider
}
