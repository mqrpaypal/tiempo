// Fondo de pantalla animado: el tiempo con su cielo animado queda detrás de los íconos
// del escritorio (técnica WorkerW/Progman, la misma de Lively y Wallpaper Engine).
// No cambia la imagen de fondo de Windows: al desactivarlo, vuelve a verse la de siempre.
package main

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	pFindWindow          = user32.NewProc("FindWindowW")
	pFindWindowEx        = user32.NewProc("FindWindowExW")
	pSendMessageTimeout  = user32.NewProc("SendMessageTimeoutW")
	pEnumWindows         = user32.NewProc("EnumWindows")
	pSetParent           = user32.NewProc("SetParent")
	pGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	pGetWindowRect       = user32.NewProc("GetWindowRect")
	pMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	pMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	pGetMonitorInfo      = user32.NewProc("GetMonitorInfoW")
	pIsZoomed            = user32.NewProc("IsZoomed")
	pIsIconic            = user32.NewProc("IsIconic")
	pGetClassName        = user32.NewProc("GetClassNameW")
	pIsWindow            = user32.NewProc("IsWindow")

	wallPanel   = &flyout{query: "fondo=1", pinned: true}
	wallOn      bool
	wallPaused  bool
	wallHost    uintptr // ventana del escritorio que contiene el fondo
	wallRetries int
)

const (
	wmWallTick = 0x8007
	idWall     = 6
)

func wallFlag(name string) string { return filepath.Join(dataDir, name) }

// Preferencia guardada: activado salvo que el usuario lo haya apagado.
func wallWanted() bool {
	_, err := os.Stat(wallFlag("fondo-desactivado"))
	return err != nil
}

func setWallWanted(on bool) {
	if on {
		os.Remove(wallFlag("fondo-desactivado"))
	} else {
		os.WriteFile(wallFlag("fondo-desactivado"), []byte("1"), 0o644)
	}
}

func className(h uintptr) string {
	var buf [64]uint16
	pGetClassName.Call(h, uintptr(unsafe.Pointer(&buf[0])), 64)
	return syscall.UTF16ToString(buf[:])
}

// desktopHost prepara el escritorio y devuelve dónde colgar el fondo y bajo qué ventana ponerlo.
func desktopHost() (parent, below uintptr) {
	progman, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(u16("Progman"))), 0)
	if progman == 0 {
		return 0, 0
	}
	// Pide al Explorador que cree la capa WorkerW detrás de los íconos.
	var res uintptr
	pSendMessageTimeout.Call(progman, 0x052C, 0xD, 0x1, 0, 1000, uintptr(unsafe.Pointer(&res)))
	pSendMessageTimeout.Call(progman, 0x052C, 0, 0, 0, 1000, uintptr(unsafe.Pointer(&res)))

	// Windows 11 24H2 y posteriores: los íconos y la capa WorkerW son hijos de Progman.
	if defView, _, _ := pFindWindowEx.Call(progman, 0, uintptr(unsafe.Pointer(u16("SHELLDLL_DefView"))), 0); defView != 0 {
		if ww, _, _ := pFindWindowEx.Call(progman, 0, uintptr(unsafe.Pointer(u16("WorkerW"))), 0); ww != 0 {
			return progman, defView
		}
	}
	// Versiones anteriores: la capa WorkerW es la ventana que sigue a la que tiene los íconos.
	var worker uintptr
	cb := syscall.NewCallback(func(h, _ uintptr) uintptr {
		if dv, _, _ := pFindWindowEx.Call(h, 0, uintptr(unsafe.Pointer(u16("SHELLDLL_DefView"))), 0); dv != 0 {
			worker, _, _ = pFindWindowEx.Call(0, h, uintptr(unsafe.Pointer(u16("WorkerW"))), 0)
		}
		return 1
	})
	pEnumWindows.Call(cb, 0)
	if worker != 0 {
		return worker, 0
	}
	return progman, 0
}

type monitorInfo struct {
	cbSize  uint32
	monitor rectT
	work    rectT
	flags   uint32
}

func primaryMonitor() rectT {
	mon, _, _ := pMonitorFromPoint.Call(0, 1) // punto (0,0), MONITOR_DEFAULTTOPRIMARY
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi.monitor
}

// attachWall cuelga el fondo detrás de los íconos y lo ajusta al monitor principal.
func attachWall() bool {
	if !wallPanel.create() {
		return false
	}
	parent, below := desktopHost()
	if parent == 0 {
		return false
	}
	h := wallPanel.hwnd
	gwlStyle, gwlEx := -16, -20
	pSetWindowLongPtr.Call(h, uintptr(gwlStyle), 0x40000000|0x10000000|0x02000000|0x04000000) // CHILD|VISIBLE|CLIPCHILDREN|CLIPSIBLINGS
	pSetWindowLongPtr.Call(h, uintptr(gwlEx), 0x08000000)                                     // NOACTIVATE
	pSetParent.Call(h, parent)
	wallHost = parent

	// Coordenadas relativas a la ventana del escritorio (que cubre todos los monitores).
	m := primaryMonitor()
	var pr rectT
	pGetWindowRect.Call(parent, uintptr(unsafe.Pointer(&pr)))
	x, y := m.l-pr.l, m.t-pr.t
	insertAfter := uintptr(0) // HWND_TOP dentro de la capa
	if below != 0 {
		insertAfter = below // justo debajo de los íconos
	}
	pSetWindowPos.Call(h, insertAfter, uintptr(x), uintptr(y), uintptr(m.r-m.l), uintptr(m.b-m.t), 0x0040|0x0020|0x0010) // SHOWWINDOW|FRAMECHANGED|NOACTIVATE
	if wallPanel.ch != nil {
		wallPanel.ch.Show()
	}
	logf("fondo de pantalla activado (%s, %dx%d)", className(parent), m.r-m.l, m.b-m.t)
	return true
}

func enableWall() {
	wallOn = true
	wallPaused = false
	if !attachWall() {
		// El Explorador puede no estar listo al iniciar Windows: reintenta.
		if wallRetries < 20 {
			wallRetries++
			time.AfterFunc(3*time.Second, func() { pPostMessage.Call(hwndMain, wmWallTick, 1, 0) })
		} else {
			logf("no se pudo colocar el fondo de pantalla")
		}
		return
	}
	wallRetries = 0
}

func disableWall() {
	wallOn = false
	if wallPanel.hwnd != 0 {
		pShowWindow.Call(wallPanel.hwnd, 0)
		if wallPanel.ch != nil {
			wallPanel.ch.Hide()
		}
	}
	refreshDesktop()
}

// refreshDesktop vuelve a dibujar la imagen de fondo de siempre (sin cambiarla).
func refreshDesktop() {
	var buf [260]uint16
	pSystemParamsInfo.Call(0x0073, 260, uintptr(unsafe.Pointer(&buf[0])), 0) // SPI_GETDESKWALLPAPER
	pSystemParamsInfo.Call(0x0014, 0, uintptr(unsafe.Pointer(&buf[0])), 0)   // SPI_SETDESKWALLPAPER, sin guardar
}

// coveredByApp: hay un juego en pantalla completa o una ventana maximizada en el monitor principal.
func coveredByApp() bool {
	fg, _, _ := pGetForegroundWindow.Call()
	if fg == 0 {
		return false
	}
	switch className(fg) {
	case "Progman", "WorkerW", "Shell_TrayWnd", "Shell_SecondaryTrayWnd":
		return false
	}
	if r, _, _ := pIsIconic.Call(fg); r != 0 {
		return false
	}
	mon, _, _ := pMonitorFromWindow.Call(fg, 2)
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
	if mi.monitor != primaryMonitor() {
		return false
	}
	if z, _, _ := pIsZoomed.Call(fg); z != 0 {
		return true
	}
	var wr rectT
	pGetWindowRect.Call(fg, uintptr(unsafe.Pointer(&wr)))
	return wr.l <= mi.monitor.l && wr.t <= mi.monitor.t && wr.r >= mi.monitor.r && wr.b >= mi.monitor.b
}

// wallTick (cada 2 s): pausa el fondo cuando no se ve, y lo reanuda al volver al escritorio.
func wallTick() {
	if !wallOn || wallPanel.ch == nil || wallPanel.hwnd == 0 {
		return
	}
	cover := coveredByApp()
	if cover && !wallPaused {
		wallPanel.ch.Hide()
		wallPaused = true
	} else if !cover && wallPaused {
		wallPanel.ch.Show()
		wallPaused = false
	}
}

func toggleWall() {
	if wallOn {
		setWallWanted(false)
		disableWall()
	} else {
		setWallWanted(true)
		enableWall()
	}
}

// resetWallIfGone: si el Explorador se reinició, la ventana del fondo desapareció con él.
func resetWallIfGone() {
	if wallPanel.hwnd == 0 {
		return
	}
	if ok, _, _ := pIsWindow.Call(wallPanel.hwnd); ok != 0 {
		return
	}
	delete(flyoutsByHwnd, wallPanel.hwnd)
	wallPanel.wv, wallPanel.ch, wallPanel.hwnd, wallPanel.failed = nil, nil, 0, false
}
