package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"nistro-ctl/internal/api"
)

type OfferSelector struct {
	table    table.Model
	offers   []api.Offer
	selected *api.Offer
	err      error
}

func NewOfferSelector(offers []api.Offer) *OfferSelector {
	columns := []table.Column{
		{Title: "Cloud", Width: 10},
		{Title: "Region", Width: 14},
		{Title: "Instance", Width: 26},
		{Title: "GPU", Width: 16},
		{Title: "Mode", Width: 14},
		{Title: "$/hr", Width: 10},
	}
	rows := make([]table.Row, 0, len(offers))
	for _, offer := range offers {
		mode := "dedicated"
		if !offer.Dedicated {
			mode = fmt.Sprintf("MIG %s", offer.MIGProfile)
		}
		rows = append(rows, table.Row{
			offer.Cloud,
			offer.Region,
			offer.SKU,
			firstNonEmpty(offer.GPUType, offer.GPU),
			mode,
			fmt.Sprintf("%.4f", offer.PricePerHourUSD),
		})
	}
	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true).Foreground(lipgloss.Color("205"))
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Bold(false)
	t.SetStyles(styles)
	return &OfferSelector{table: t, offers: offers}
}

func (m *OfferSelector) Init() tea.Cmd { return nil }

func (m *OfferSelector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.err = fmt.Errorf("selection cancelled")
			return m, tea.Quit
		case "enter":
			if len(m.offers) > 0 {
				selected := m.offers[m.table.Cursor()]
				m.selected = &selected
			}
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *OfferSelector) View() string {
	var b strings.Builder
	b.WriteString("Select hardware for this vLLM deployment\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n\nenter accept  q cancel\n")
	return b.String()
}

func (m *OfferSelector) Selected() (api.Offer, error) {
	if m.err != nil {
		return api.Offer{}, m.err
	}
	if m.selected == nil {
		return api.Offer{}, fmt.Errorf("no offer selected")
	}
	return *m.selected, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
