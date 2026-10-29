// Paneles sin bordes hechos con WebView2:
//   - "reloj": panel chico abajo a la derecha, sobre la bandeja.
//   - "lateral": panel amplio pegado al borde izquierdo, de arriba a abajo (pronóstico completo).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
)

const (
	wmPanelHeight = 0x8004
	wmPanelHide   = 0x8005
	wmSideOpen    = 0x8006
)

var (
	pSetWindowLongPtr   = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProc     = user32.NewProc("CallWindowProcW")
	pSetWindowPos       = user32.NewProc("SetWindowPos")
	pShowWindow         = user32.NewProc("ShowWindow")
	pIsWindowVisible    = user32.NewProc("IsWindowVisible")
	pSystemParamsInfo   = user32.NewProc("SystemParametersInfoW")
	pGetDpiForWindow    = user32.NewProc("GetDpiForWindow")
	pSetClassLongPtr    = user32.NewProc("SetClassLongPtrW")
	pGetClassLongPtr    = user32.NewProc("GetClassLongPtrW")
	pDwmSetWindowAttrib = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

	webviewOK     = -1 // -1 sin revisar, 0 no disponible, 1 disponible
	flyoutProc    uintptr
	flyoutsByHwnd = map[uintptr]*flyout{}
)

type flyout struct {
	query   string // parámetro de la página
	cssW    int
	cssH    int // solo panel del reloj (alto según contenido)
	side    bool
	pinned  bool // fondo de pantalla: no se esconde al perder el foco
	wv      webview2.WebView
	ch      *edge.Chromium
	hwnd    uintptr
	oldProc uintptr
	making  bool
	failed  bool
	hidAt   time.Time
	loaded  time.Time
}

var (
	clockPanel = &flyout{query: "panel=1", cssW: 400, cssH: 470}
	sidePanel  = &flyout{query: "lateral=1", cssW: 560, cssH: 830, side: true}
)

// ¿Está instalado el runtime de WebView2?
func hasWebView2() bool {
	if webviewOK >= 0 {
		return webviewOK == 1
	}
	webviewOK = 0
	keys := []struct {
		root syscall.Handle
		path string
	}{
		{syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
		{syscall.HKEY_CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`},
	}
	for _, k := range keys {
		var h syscall.Handle
		if syscall.RegOpenKeyEx(k.root, u16(k.path), 0, syscall.KEY_READ, &h) == nil {
			var buf [64]uint16
			n := uint32(len(buf) * 2)
			var typ uint32
			ok := syscall.RegQueryValueEx(h, u16("pv"), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &n) == nil
			syscall.RegCloseKey(h)
			if v := syscall.UTF16ToString(buf[:]); ok && v != "" && v != "0.0.0.0" {
				webviewOK = 1
				return true
			}
		}
	}
	return false
}

func flyoutWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	f := flyoutsByHwnd[hwnd]
	if f == nil {
		return 0
	}
	switch msg {
	case 0x0010: // WM_CLOSE: solo se esconde
		if !f.pinned {
			f.hide()
		}
		return 0
	case 0x0006: // WM_ACTIVATE
		if wp&0xffff == 0 && !f.pinned { // clic fuera del panel
			f.hide()
		}
	case 0x0021: // WM_MOUSEACTIVATE: el fondo nunca toma el foco
		if f.pinned {
			return 3 // MA_NOACTIVATE
		}
	}
	r, _, _ := pCallWindowProc.Call(f.oldProc, hwnd, msg, wp, lp)
	return r
}

func (f *flyout) create() bool {
	if f.failed || f.making {
		return false
	}
	if f.wv != nil {
		return true
	}
	if !hasWebView2() {
		logf("WebView2 no está instalado: se usa la ventana normal")
		f.failed = true
		return false
	}
	f.making = true
	defer func() { f.making = false }()
	ok := func() (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				logf("error al crear panel %s: %v", f.query, r)
				ok = false
			}
		}()
		f.wv = webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      dataDir + `\webview2-v2`,
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: "Tiempo Temuco", Width: 1, Height: 1},
		})
		return f.wv != nil
	}()
	if !ok {
		logf("no se pudo crear el panel %s", f.query)
		f.wv = nil
		f.failed = true
		return false
	}
	logf("panel %s creado", f.query)
	f.hwnd = uintptr(f.wv.Window())
	disableZoom(f.wv)
	f.ch = chromiumOf(f.wv)
	pShowWindow.Call(f.hwnd, 0) // SW_HIDE

	// Ventana emergente sin bordes, sin botón en la barra de tareas y siempre encima.
	gwlStyle, gwlEx, gclStyle, gwlpWndProc := -16, -20, -26, -4
	pSetWindowLongPtr.Call(f.hwnd, uintptr(gwlStyle), 0x80000000|0x02000000) // WS_POPUP | WS_CLIPCHILDREN
	pSetWindowLongPtr.Call(f.hwnd, uintptr(gwlEx), 0x80|0x8)                 // TOOLWINDOW | TOPMOST
	cs, _, _ := pGetClassLongPtr.Call(f.hwnd, uintptr(gclStyle))
	pSetClassLongPtr.Call(f.hwnd, uintptr(gclStyle), cs|0x00020000) // CS_DROPSHADOW
	round := uint32(2)                                              // esquinas redondeadas en Windows 11
	pDwmSetWindowAttrib.Call(f.hwnd, 33, uintptr(unsafe.Pointer(&round)), 4)

	if flyoutProc == 0 {
		flyoutProc = syscall.NewCallback(flyoutWndProc)
	}
	flyoutsByHwnd[f.hwnd] = f
	f.oldProc, _, _ = pSetWindowLongPtr.Call(f.hwnd, uintptr(gwlpWndProc), flyoutProc)

	f.wv.Navigate("http://" + addr + "/?" + f.query)
	f.loaded = time.Now()
	return true
}

func (f *flyout) visible() bool {
	if f.hwnd == 0 {
		return false
	}
	r, _, _ := pIsWindowVisible.Call(f.hwnd)
	return r != 0
}

// Ajustes de los paneles que se pueden cambiar sin reiniciar el programa.
type panelCfg struct {
	Lado         string `json:"lateral"`      // "derecha" o "izquierda"
	AnchoLateral int    `json:"anchoLateral"` // px CSS
	AnchoReloj   int    `json:"anchoReloj"`   // px CSS
}

func readPanelCfg() panelCfg {
	c := panelCfg{Lado: "derecha", AnchoLateral: 560, AnchoReloj: 400}
	if b, err := os.ReadFile(dataDir + `\paneles.json`); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// Registro sencillo para diagnosticar problemas en el equipo.
func logf(format string, a ...interface{}) {
	f, err := os.OpenFile(dataDir+`\registro.txt`, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

func (f *flyout) place() {
	if f.hwnd == 0 {
		return
	}
	cfg := readPanelCfg()
	if f.side && cfg.AnchoLateral >= 300 {
		f.cssW = cfg.AnchoLateral
	} else if !f.side && cfg.AnchoReloj >= 280 {
		f.cssW = cfg.AnchoReloj
	}
	var wa rectT
	pSystemParamsInfo.Call(0x30, 0, uintptr(unsafe.Pointer(&wa)), 0) // SPI_GETWORKAREA
	dpi, _, _ := pGetDpiForWindow.Call(f.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	sc := float64(dpi) / 96
	m := int32(12 * sc)
	w := int32(float64(f.cssW) * sc)
	if maxW := wa.r - wa.l - 2*m; w > maxW {
		w = maxW
	}
	var x, y, h int32
	// Alto según el contenido, apoyado abajo sobre la barra de tareas.
	h = int32(float64(f.cssH) * sc)
	if maxH := wa.b - wa.t - 2*m; h > maxH {
		h = maxH
	}
	y = wa.b - h - m
	if f.side && cfg.Lado == "izquierda" {
		x = wa.l + m
	} else {
		x = wa.r - w - m
	}
	pSetWindowPos.Call(f.hwnd, ^uintptr(0) /*HWND_TOPMOST*/, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		0x0020|0x0010) // FRAMECHANGED | NOACTIVATE
}

func (f *flyout) show() bool {
	if f.making {
		return true
	}
	if !f.create() {
		return false
	}
	f.place()
	if f.ch != nil {
		f.ch.Show() // la página vuelve a estar visible: reanuda las animaciones
	}
	pShowWindow.Call(f.hwnd, 5) // SW_SHOW
	pSetForegroundWindow.Call(f.hwnd)
	if time.Since(f.loaded) > 5*time.Minute { // datos frescos
		f.wv.Eval(`typeof load === "function" && load()`)
		f.loaded = time.Now()
	}
	return true
}

func (f *flyout) hide() {
	if f.visible() {
		pShowWindow.Call(f.hwnd, 0)
		if f.ch != nil {
			f.ch.Hide() // la página queda oculta: las animaciones se detienen
		}
		f.hidAt = time.Now()
	}
}

func hideAllPanels() {
	clockPanel.hide()
	sidePanel.hide()
}

// Clic en el ícono: abre o cierra el panel del reloj.
func togglePanel() {
	if clockPanel.visible() {
		clockPanel.hide()
		return
	}
	// El clic en la bandeja ya lo escondió (perdió el foco): no reabrir.
	if time.Since(clockPanel.hidAt) < 400*time.Millisecond {
		return
	}
	showPanel()
}

func showPanel() {
	sidePanel.hide()
	if !clockPanel.show() {
		openWindow() // sin WebView2: ventana normal como antes
	}
}

// Pronóstico completo en el panel lateral izquierdo.
func showSidePanel() {
	clockPanel.hide()
	if !sidePanel.show() {
		openWindow()
	}
}

// Compatibilidad con el resto del programa.
func placePanel()        { clockPanel.place() }
func panelVisible() bool { return clockPanel.visible() }
func hidePanel()         { hideAllPanels() }
func setPanelHeight(h int, side bool) {
	f := clockPanel
	if side {
		f = sidePanel
	}
	f.cssH = h
	if f.visible() {
		f.place()
	}
}

// disableZoom desactiva el zoom con Ctrl + rueda, Ctrl + / Ctrl - y el gesto de pellizco,
// que descuadraban el panel. La librería no expone esta opción, así que se accede a ella aquí.
func chromiumOf(wv webview2.WebView) (ch *edge.Chromium) {
	defer func() {
		if r := recover(); r != nil {
			logf("no se pudo acceder al navegador del panel: %v", r)
			ch = nil
		}
	}()
	v := reflect.ValueOf(wv)
	if v.Kind() != reflect.Ptr {
		return nil
	}
	fv := v.Elem().FieldByName("browser")
	if !fv.IsValid() {
		return nil
	}
	b := reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem().Interface()
	ch, _ = b.(*edge.Chromium)
	return ch
}

func disableZoom(wv webview2.WebView) {
	ch := chromiumOf(wv)
	if ch == nil {
		return
	}
	if st, err := ch.GetSettings(); err == nil && st != nil {
		st.PutIsZoomControlEnabled(false)
		st.PutIsPinchZoomEnabled(false)
	}
}
