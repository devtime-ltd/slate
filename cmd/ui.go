package cmd

import "github.com/charmbracelet/lipgloss"

var (
	tickStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	crossStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
)

func tick() string  { return tickStyle.Render("✔") }
func cross() string { return crossStyle.Render("✘") }
func warn() string  { return warnStyle.Render("!") }
