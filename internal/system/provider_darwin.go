//go:build darwin

package system

import "fmt"

type darwinProvider struct{}

func newProvider() SystemProvider {
	return &darwinProvider{}
}

func (p *darwinProvider) EnterLockMode(hwnd uintptr) error {
	return fmt.Errorf("not implemented on darwin")
}

func (p *darwinProvider) ExitLockMode() error {
	return fmt.Errorf("not implemented on darwin")
}

func (p *darwinProvider) OnWindowClosing() bool { return true }

func (p *darwinProvider) StartTray(onShow, onQuit func()) error {
	return fmt.Errorf("not implemented on darwin")
}

func (p *darwinProvider) StopTray() error {
	return fmt.Errorf("not implemented on darwin")
}

func (p *darwinProvider) AcquireLock() (bool, error) {
	return true, nil
}

func (p *darwinProvider) SetAutoStart(enabled bool) error {
	return fmt.Errorf("not implemented on darwin")
}

func (p *darwinProvider) FindFlutterWindow() (uintptr, error) {
	return 0, fmt.Errorf("not implemented on darwin")
}
func (p *darwinProvider) SetServerInfo(healthURL string, flutterPID int) {}
func (p *darwinProvider) FullscreenFlutterWindow() error {
	return fmt.Errorf("not implemented")
}
func (p *darwinProvider) RestoreFlutterWindow() error {
	return fmt.Errorf("not implemented")
}
func (p *darwinProvider) SetPassiveInterval(minutes int) {}
func (p *darwinProvider) StartPassiveQuiz()              {}
func (p *darwinProvider) StopPassiveQuiz()               {}
