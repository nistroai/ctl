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

type launchEventsSnapshot struct {
	events []api.LaunchEvent
	err    error
}

type postgresEventsSnapshot struct {
	events []api.PostgresDeploymentEvent
	err    error
}

type LaunchEventsModel struct {
	deploymentID string
	events       []api.LaunchEvent
	err          error
	updatedAt    time.Time
}

type PostgresEventsModel struct {
	deploymentID string
	events       []api.PostgresDeploymentEvent
	err          error
	updatedAt    time.Time
}

func NewLaunchEventsModel(deploymentID string) *LaunchEventsModel {
	return &LaunchEventsModel{deploymentID: deploymentID}
}

func NewPostgresEventsModel(deploymentID string) *PostgresEventsModel {
	return &PostgresEventsModel{deploymentID: deploymentID}
}

func (m *LaunchEventsModel) Init() tea.Cmd {
	return nil
}

func (m *PostgresEventsModel) Init() tea.Cmd {
	return nil
}

func (m *LaunchEventsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
	case launchEventsSnapshot:
		m.events = msg.events
		m.err = msg.err
		m.updatedAt = time.Now()
	}
	return m, nil
}

func (m *PostgresEventsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
	case postgresEventsSnapshot:
		m.events = msg.events
		m.err = msg.err
		m.updatedAt = time.Now()
	}
	return m, nil
}

func (m *LaunchEventsModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Nistro vLLM Launch Events")
	lines := []string{title, "", "Deployment " + m.deploymentID}
	if !m.updatedAt.IsZero() {
		lines = append(lines, "Updated    "+m.updatedAt.Format(time.RFC3339))
	}
	if m.err != nil {
		lines = append(lines, "", "Error      "+m.err.Error(), "", "q quit")
		return strings.Join(lines, "\n") + "\n"
	}
	if len(m.events) == 0 {
		lines = append(lines, "", "Waiting for launch events...", "", "q quit")
		return strings.Join(lines, "\n") + "\n"
	}
	lines = append(lines, "")
	for _, event := range m.events {
		lines = append(lines, fmt.Sprintf("%s  %-24s %-7s %s",
			event.ObservedAt.Local().Format("15:04:05"),
			event.EventKey,
			event.Level,
			emptyDash(event.Message),
		))
	}
	lines = append(lines, "", "q quit")
	return strings.Join(lines, "\n") + "\n"
}

func (m *PostgresEventsModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("Nistro Postgres Events")
	lines := []string{title, "", "Deployment " + m.deploymentID}
	if !m.updatedAt.IsZero() {
		lines = append(lines, "Updated    "+m.updatedAt.Format(time.RFC3339))
	}
	if m.err != nil {
		lines = append(lines, "", "Error      "+m.err.Error(), "", "q quit")
		return strings.Join(lines, "\n") + "\n"
	}
	if len(m.events) == 0 {
		lines = append(lines, "", "Waiting for deployment events...", "", "q quit")
		return strings.Join(lines, "\n") + "\n"
	}
	lines = append(lines, "")
	for _, event := range m.events {
		lines = append(lines, fmt.Sprintf("%s  %-18s %s",
			event.CreatedAt.Local().Format("15:04:05"),
			emptyDash(event.Status),
			emptyDash(event.Message),
		))
	}
	lines = append(lines, "", "q quit")
	return strings.Join(lines, "\n") + "\n"
}

func StreamLaunchEvents(ctx context.Context, program *tea.Program, client *api.Client, deploymentID string, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		sendLaunchEvents(ctx, program, client, deploymentID)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func StreamPostgresEvents(ctx context.Context, program *tea.Program, client *api.Client, deploymentID string, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		sendPostgresEvents(ctx, program, client, deploymentID)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sendLaunchEvents(ctx context.Context, program *tea.Program, client *api.Client, deploymentID string) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events, err := client.LaunchEvents(reqCtx, deploymentID)
	program.Send(launchEventsSnapshot{events: events, err: err})
}

func sendPostgresEvents(ctx context.Context, program *tea.Program, client *api.Client, deploymentID string) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events, err := client.PostgresEvents(reqCtx, deploymentID)
	program.Send(postgresEventsSnapshot{events: events, err: err})
}
