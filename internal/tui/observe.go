package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"nistro-ctl/internal/api"
)

type observeTick struct{}

type observeResult struct {
	summary api.ObserveSummary
	err     error
}

type ObserveModel struct {
	client   *api.Client
	endpoint string
	apiKey   string
	summary  api.ObserveSummary
	err      error
}

func NewObserveModel(client *api.Client, endpoint, apiKey string) *ObserveModel {
	return &ObserveModel{client: client, endpoint: endpoint, apiKey: apiKey}
}

func (m *ObserveModel) Init() tea.Cmd {
	return tea.Batch(m.fetch(), tick())
}

func (m *ObserveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case observeTick:
		return m, tea.Batch(m.fetch(), tick())
	case observeResult:
		m.summary = msg.summary
		m.err = msg.err
	}
	return m, nil
}

func (m *ObserveModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Nistro vLLM Observability")
	if m.err != nil {
		return title + "\n\n" + m.err.Error() + "\n\nq quit\n"
	}
	s := m.summary
	lines := []string{
		title,
		"",
		fmt.Sprintf("Tokens/sec      %.2f total | %.2f prompt | %.2f output", s.TokensPerSecond, s.PromptTokensPerSec, s.OutputTokensPerSec),
		fmt.Sprintf("Requests        %.0f running | %.0f waiting", s.RunningRequests, s.WaitingRequests),
		fmt.Sprintf("Context         %d tokens | KV/cache %.1f%% full", s.ContextLength, s.KVCacheUsagePercent),
		fmt.Sprintf("GPU             %.1f%% util | VRAM %.2f / %.2f GiB", s.GPUUtilization, s.VRAMUsedGB, s.VRAMTotalGB),
		fmt.Sprintf("Placement       %s | fraction %.3f", emptyDash(s.MIGProfile), s.GPUFraction),
		fmt.Sprintf("Last sample     %s", s.Timestamp.Format(time.RFC3339)),
		"",
		"q quit",
	}
	return strings.Join(lines, "\n")
}

func (m *ObserveModel) fetch() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		summary, err := m.client.Observe(ctx, m.endpoint, m.apiKey)
		return observeResult{summary: summary, err: err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return observeTick{} })
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
