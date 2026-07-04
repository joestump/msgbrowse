//go:build desktop && !darwin

// Non-macOS platform glue for the menubar status item and Dock policy (issue
// #167). Off macOS there is nothing to sequence — fyne.io/systray's Linux
// backend is pure-Go D-Bus (StatusNotifierItem), fully independent of the
// GUI toolkit's run loop — and there is no Dock, so the policy hooks are
// no-ops. See tray_platform_darwin.go for the macOS behavior these mirror.
package main

// scheduleTrayStart registers the status item immediately: the D-Bus backend
// has no main-thread or app-launch ordering requirement.
func scheduleTrayStart(start func()) { start() }

// setDockVisible is macOS-only (Dock activation policy); no-op here.
func setDockVisible(bool) {}

// enableDockAutoHide is macOS-only; no-op here.
func enableDockAutoHide() {}
