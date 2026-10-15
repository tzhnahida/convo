package system

type SystemProvider interface {
	EnterLockMode(hwnd uintptr) error
	ExitLockMode() error
	OnWindowClosing() bool
	StartTray(onShow, onQuit func()) error
	StopTray() error
	AcquireLock() (bool, error)
	SetAutoStart(enabled bool) error
	FindFlutterWindow() (uintptr, error)
	// Runtime configuration
	SetServerInfo(healthURL string, flutterPID int)
	// Flutter window control
	FullscreenFlutterWindow() error
	RestoreFlutterWindow() error
	// Passive quiz
	SetPassiveInterval(minutes int)
	StartPassiveQuiz()
	StopPassiveQuiz()
}

// Default returns the platform-specific SystemProvider.
var Default SystemProvider = newProvider()
