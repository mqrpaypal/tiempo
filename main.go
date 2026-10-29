// Tiempo Temuco: ícono en la bandeja de Windows con la temperatura actual
// y una ventana con el pronóstico completo (Edge o Chrome en modo app).
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

//go:embed index.html
var page []byte

//go:embed icon.ico
var favicon []byte

const addr = "127.0.0.1:47813"

// ---------- estado compartido ----------

type Place struct {
	Name   string  `json:"name"`
	Region string  `json:"region"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

var (
	mu       sync.Mutex
	place    = Place{"Temuco", "Araucanía, Chile", -38.7359, -72.5904}
	curTemp  = math.NaN()
	curFeels = math.NaN()
	curCode  = -1
	curDay   = true
	curErr   bool
	dataDir  string
	hwndMain uintptr
)

func cfgPath() string { return filepath.Join(dataDir, "ciudad.json") }

func loadPlace() {
	b, err := os.ReadFile(cfgPath())
	if err != nil {
		return
	}
	var p Place
	if json.Unmarshal(b, &p) == nil && p.Name != "" {
		place = p
	}
}

func savePlace(p Place) {
	b, _ := json.Marshal(p)
	os.WriteFile(cfgPath(), b, 0o644)
}

// ---------- Win32 ----------

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pPostMessage         = user32.NewProc("PostMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pSetTimer            = user32.NewProc("SetTimer")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pAppendMenu          = user32.NewProc("AppendMenuW")
	pTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pRegisterWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	pCreateIconIndirect  = user32.NewProc("CreateIconIndirect")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	pDrawText            = user32.NewProc("DrawTextW")
	pMessageBox          = user32.NewProc("MessageBoxW")
	pSetDpiAwarenessCtx  = user32.NewProc("SetProcessDpiAwarenessContext")
	pCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap        = gdi32.NewProc("CreateBitmap")
	pSelectObject        = gdi32.NewProc("SelectObject")
	pDeleteObject        = gdi32.NewProc("DeleteObject")
	pDeleteDC            = gdi32.NewProc("DeleteDC")
	pCreateFont          = gdi32.NewProc("CreateFontW")
	pSetTextColor        = gdi32.NewProc("SetTextColor")
	pSetBkMode           = gdi32.NewProc("SetBkMode")
	pGetTextExtent       = gdi32.NewProc("GetTextExtentPoint32W")
	pShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	pRegSetValueEx       = advapi32.NewProc("RegSetValueExW")
)

const (
	wmDestroy  = 0x0002
	wmCommand  = 0x0111
	wmTimer    = 0x0113
	wmLBUp     = 0x0202
	wmRBUp     = 0x0205
	wmTray     = 0x8001
	wmRefresh  = 0x8002
	wmFetch    = 0x8003
	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 1
	nifIcon    = 2
	nifTip     = 4
	idOpen     = 1
	idRefresh  = 2
	idExit     = 3
	idStartup  = 4
	idFull     = 5
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgT struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x, y    int32
	priv    uint32
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

type sizeT struct{ cx, cy int32 }
type rectT struct{ l, t, r, b int32 }

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func msgBox(text string) {
	pMessageBox.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16("Tiempo Temuco"))), 0x40)
}

// ¿La barra de tareas usa tema claro? Entonces el número va en oscuro.
func lightTaskbar() bool {
	var k syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER,
		u16(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`), 0, syscall.KEY_READ, &k) != nil {
		return false
	}
	defer syscall.RegCloseKey(k)
	var v, typ uint32
	n := uint32(4)
	if syscall.RegQueryValueEx(k, u16("SystemUsesLightTheme"), nil, &typ, (*byte)(unsafe.Pointer(&v)), &n) != nil {
		return false
	}
	return v == 1
}

// inkDigits dibuja el texto con Segoe UI en negrita y devuelve su máscara recortada al trazo.
func inkDigits(label string, h int32) (mask []float64, w, hh int) {
	M := int32(h*2 + 8)
	dc, _, _ := pCreateCompatibleDC.Call(0)
	defer pDeleteDC.Call(dc)
	bi := bitmapInfoHeader{biSize: 40, biWidth: M, biHeight: -M, biPlanes: 1, biBitCount: 32}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return nil, 0, 0
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dc, bmp)
	pSetBkMode.Call(dc, 1)
	pSetTextColor.Call(dc, 0x00FFFFFF)
	f, _, _ := pCreateFont.Call(uintptr(-h), 0, 0, 0, 700, 0, 0, 0, 1, 0, 0, 4 /*ANTIALIASED*/, 0, uintptr(unsafe.Pointer(u16("Segoe UI"))))
	of, _, _ := pSelectObject.Call(dc, f)
	r := rectT{2, 2, M, M}
	w16 := u16(label)
	pDrawText.Call(dc, uintptr(unsafe.Pointer(w16)), uintptr(len([]rune(label))), uintptr(unsafe.Pointer(&r)), 0x20) // SINGLELINE
	pSelectObject.Call(dc, of)
	pDeleteObject.Call(f)
	pSelectObject.Call(dc, old)

	px := unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(M*M*4))
	x0, y0, x1, y1 := int(M), int(M), -1, -1
	for y := 0; y < int(M); y++ {
		for x := 0; x < int(M); x++ {
			k := (y*int(M) + x) * 4
			if px[k] > 24 || px[k+1] > 24 || px[k+2] > 24 {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 {
		return nil, 0, 0
	}
	w, hh = x1-x0+1, y1-y0+1
	mask = make([]float64, w*hh)
	for y := 0; y < hh; y++ {
		for x := 0; x < w; x++ {
			k := ((y+y0)*int(M) + x + x0) * 4
			v := max(px[k], px[k+1], px[k+2])
			mask[y*w+x] = float64(v) / 255
		}
	}
	return mask, w, hh
}

// makeTrayIcon: dibujo del cielo arriba a la derecha y la temperatura con ° abajo, en un solo ícono.
// tempColor: color del número según la temperatura (azul frío → rojo calor).
// En barra clara se usan tonos más oscuros para que se lean.
func tempColor(t float64, light bool) rgb {
	type step struct {
		max         float64
		dark, clear uint32
	}
	steps := []step{
		{5, 0x5AA9FF, 0x1F6FD1},   // bajo 5°: azul
		{12, 0x9FD4FF, 0x2B8BC9},  // 5° a 12°: celeste
		{22, 0xFFFFFF, 0x1A1A1A},  // 13° a 22°: blanco (o negro en barra clara)
		{28, 0xFFB347, 0xC46A00},  // 23° a 28°: naranjo
		{1e9, 0xFF6B57, 0xC8322A}, // sobre 28°: rojo
	}
	if math.IsNaN(t) {
		t = 15
	}
	for _, s := range steps {
		if math.Round(t) <= s.max {
			if light {
				return hex(s.clear)
			}
			return hex(s.dark)
		}
	}
	return hex(0xFFFFFF)
}

func makeTrayIcon(text string, code int, day bool, temp float64) uintptr {
	n := int(trayIconSize())
	light := lightTaskbar()
	cv := newCanvas(n)

	// 1) Dibujo del cielo en la esquina superior derecha.
	gs := 0.62
	drawWeatherOn(cv, code, day, light, 1-gs, 0, gs)

	// 2) Número: el más grande que quepa en la franja inferior, con espacio para el °.
	label := strings.TrimSuffix(text, "°")
	withDeg := label != text
	boxTop := int(math.Round(float64(n) * 0.36))
	boxH := n - boxTop
	var dm []float64
	var dw, dh int
	for h := int32(n); h >= 6; h-- {
		m, w, hh := inkDigits(label, h)
		if m == nil {
			continue
		}
		ring := 0
		if withDeg {
			ring = int(math.Ceil(float64(hh)*0.42)) + 1
		}
		if w+ring <= n && hh <= boxH {
			dm, dw, dh = m, w, hh
			break
		}
	}
	ink := make([]float64, n*n)
	if dm != nil {
		rad := math.Max(1.4, float64(dh)*0.15)
		th := math.Max(1.1, float64(dh)*0.11)
		ringW := 0.0
		if withDeg {
			ringW = 2*rad + th + 1
		}
		total := float64(dw) + ringW
		left := int(math.Floor((float64(n) - total) / 2))
		if left < 0 {
			left = 0
		}
		top := n - dh
		for y := 0; y < dh; y++ {
			for x := 0; x < dw; x++ {
				if xx, yy := x+left, y+top; xx < n && yy < n {
					ink[yy*n+xx] = dm[y*dw+x]
				}
			}
		}
		if withDeg {
			cx := float64(left+dw) + 1 + rad + th/2
			cy := float64(top) + rad + th/2
			for py := 0; py < n; py++ {
				for qx := 0; qx < n; qx++ {
					if cov := ringCoverage(qx, py, cx, cy, rad, th); cov > ink[py*n+qx] {
						ink[py*n+qx] = cov
					}
				}
			}
		}
	}

	// 3) Contorno del color de la barra para separar el número del dibujo, y luego el número.
	fg, halo := tempColor(temp, light), hex(0x1C1C1C)
	if light {
		halo = hex(0xF0F0F0)
	}
	cv.fillMask(dilate(ink, n, math.Max(1, float64(n)/18)), halo)
	cv.fillMask(ink, fg)
	return iconFromPixels(int32(n), cv.bgra())
}

// iconFromPixels crea un ícono de Windows a partir de píxeles BGRA.
func iconFromPixels(n int32, pix []byte) uintptr {
	dc, _, _ := pCreateCompatibleDC.Call(0)
	defer pDeleteDC.Call(dc)
	bi := bitmapInfoHeader{biSize: 40, biWidth: n, biHeight: -n, biPlanes: 1, biBitCount: 32}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return 0
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(n*n*4)), pix)
	mask, _, _ := pCreateBitmap.Call(uintptr(n), uintptr(n), 1, 1, 0)
	ii := iconInfo{fIcon: 1, hbmMask: mask, hbmColor: bmp}
	icon, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	pDeleteObject.Call(mask)
	pDeleteObject.Call(bmp)
	return icon
}

func trayIconSize() int32 {
	if r, _, _ := pGetSystemMetrics.Call(49); r > 0 { // SM_CXSMICON
		return int32(r)
	}
	return 16
}

var (
	nid        notifyIconData
	curIcon    uintptr
	taskbarMsg uintptr
)

var codes = map[int]string{
	0: "Despejado", 1: "Mayormente despejado", 2: "Parcialmente nublado", 3: "Nublado",
	45: "Neblina", 48: "Neblina", 51: "Llovizna débil", 53: "Llovizna", 55: "Llovizna intensa",
	56: "Llovizna helada", 57: "Llovizna helada", 61: "Lluvia débil", 63: "Lluvia", 65: "Lluvia intensa",
	66: "Lluvia helada", 67: "Lluvia helada", 71: "Nieve débil", 73: "Nieve", 75: "Nieve intensa", 77: "Granizo fino",
	80: "Chubascos", 81: "Chubascos", 82: "Chubascos fuertes", 85: "Chubascos de nieve", 86: "Chubascos de nieve",
	95: "Tormenta", 96: "Tormenta con granizo", 99: "Tormenta con granizo",
}

func updateTray(add bool) {
	mu.Lock()
	t, f, c, e, p, day := curTemp, curFeels, curCode, curErr, place, curDay
	mu.Unlock()

	label, tip := "--", p.Name+": cargando…"
	if !math.IsNaN(t) {
		label = fmt.Sprintf("%d°", int(math.Round(t)))
		tip = fmt.Sprintf("%s: %d°C, %s (sensación %d°)", p.Name, int(math.Round(t)), codes[c], int(math.Round(f)))
		if e {
			tip += " · sin conexión"
		}
	} else if e {
		tip = p.Name + ": sin conexión"
	}
	icon := makeTrayIcon(label, map[bool]int{true: -1, false: c}[math.IsNaN(t)], day, t)

	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwndMain
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmTray
	nid.hIcon = icon
	nid.szTip = [128]uint16{}
	tw, _ := syscall.UTF16FromString(tip)
	copy(nid.szTip[:127], tw)

	op := uintptr(nimModify)
	if add {
		op = nimAdd
	}
	if r, _, _ := pShellNotifyIcon.Call(op, uintptr(unsafe.Pointer(&nid))); r == 0 && !add {
		pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	}
	if curIcon != 0 {
		pDestroyIcon.Call(curIcon)
	}
	curIcon = icon

}

// En Windows 11 los íconos nuevos quedan ocultos tras la flecha "^"; esto lo deja visible.
func promoteTrayIcon() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	var root syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, u16(`Control Panel\NotifyIconSettings`), 0,
		syscall.KEY_READ, &root) != nil {
		return
	}
	defer syscall.RegCloseKey(root)
	for i := uint32(0); ; i++ {
		var name [256]uint16
		n := uint32(len(name))
		if syscall.RegEnumKeyEx(root, i, &name[0], &n, nil, nil, nil, nil) != nil {
			return
		}
		sub := syscall.UTF16ToString(name[:n])
		var k syscall.Handle
		if syscall.RegOpenKeyEx(root, u16(sub), 0, syscall.KEY_READ|syscall.KEY_WRITE, &k) != nil {
			continue
		}
		var buf [1024]uint16
		bl := uint32(len(buf) * 2)
		var typ uint32
		if syscall.RegQueryValueEx(k, u16("ExecutablePath"), nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &bl) == nil &&
			strings.EqualFold(syscall.UTF16ToString(buf[:]), exe) {
			one := uint32(1)
			pRegSetValueEx.Call(uintptr(k), uintptr(unsafe.Pointer(u16("IsPromoted"))), 0, 4, /*REG_DWORD*/
				uintptr(unsafe.Pointer(&one)), 4)
		}
		syscall.RegCloseKey(k)
	}
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runName = "TiempoTemuco"

var (
	pRegDeleteValue = advapi32.NewProc("RegDeleteValueW")
	pCheckMenuItem  = user32.NewProc("CheckMenuItem")
)

func startupEnabled() bool {
	var k syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, u16(runKey), 0, syscall.KEY_READ, &k) != nil {
		return false
	}
	defer syscall.RegCloseKey(k)
	var typ uint32
	n := uint32(0)
	return syscall.RegQueryValueEx(k, u16(runName), nil, &typ, nil, &n) == nil
}

func setStartup(on bool) {
	var k syscall.Handle
	if syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, u16(runKey), 0, syscall.KEY_SET_VALUE, &k) != nil {
		return
	}
	defer syscall.RegCloseKey(k)
	if !on {
		pRegDeleteValue.Call(uintptr(k), uintptr(unsafe.Pointer(u16(runName))))
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	v, _ := syscall.UTF16FromString(`"` + exe + `" --inicio`)
	pRegSetValueEx.Call(uintptr(k), uintptr(unsafe.Pointer(u16(runName))), 0, 1, /*REG_SZ*/
		uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
}

func showMenu(hwnd uintptr) {
	m, _, _ := pCreatePopupMenu.Call()
	pAppendMenu.Call(m, 0, idOpen, uintptr(unsafe.Pointer(u16("Ver panel del tiempo"))))
	pAppendMenu.Call(m, 0, idFull, uintptr(unsafe.Pointer(u16("Pronóstico completo (7 días)"))))
	pAppendMenu.Call(m, 0, idRefresh, uintptr(unsafe.Pointer(u16("Actualizar ahora"))))
	chk := uintptr(0)
	if startupEnabled() {
		chk = 0x8 // MF_CHECKED
	}
	pAppendMenu.Call(m, chk, idStartup, uintptr(unsafe.Pointer(u16("Iniciar con Windows"))))
	pAppendMenu.Call(m, 0x800, 0, 0) // separador
	pAppendMenu.Call(m, 0, idExit, uintptr(unsafe.Pointer(u16("Salir"))))
	var pt struct{ x, y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)
	pTrackPopupMenu.Call(m, 0x20 /*RIGHTBUTTON*/, uintptr(pt.x), uintptr(pt.y), 0, hwnd, 0)
	pDestroyMenu.Call(m)
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch {
	case msg == wmTray:
		switch lParam & 0xffff {
		case wmLBUp:
			togglePanel()
		case wmRBUp:
			showMenu(hwnd)
		}
		return 0
	case msg == wmCommand:
		switch wParam & 0xffff {
		case idOpen:
			showPanel()
		case idFull:
			showSidePanel()
		case idRefresh:
			go fetchWeather()
		case idStartup:
			setStartup(!startupEnabled())
		case idExit:
			pDestroyWindow.Call(hwnd)
		}
		return 0
	case msg == wmTimer || msg == wmFetch:
		go fetchWeather()
		return 0
	case msg == wmPanelHeight:
		if h := int(wParam); h > 100 && h < 2000 {
			setPanelHeight(h, lParam == 1)
		}
		return 0
	case msg == wmSideOpen:
		showSidePanel()
		return 0
	case msg == wmPanelHide:
		hidePanel()
		return 0
	case msg == wmRefresh:
		updateTray(false)
		return 0
	case taskbarMsg != 0 && msg == taskbarMsg: // el Explorador se reinició
		updateTray(true)
		return 0
	case msg == wmDestroy:
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

// ---------- datos del tiempo ----------

var fetchMu sync.Mutex

func fetchWeather() {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	mu.Lock()
	p := place
	mu.Unlock()
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%f&longitude=%f"+
		"&current=temperature_2m,apparent_temperature,weather_code,is_day&timezone=auto", p.Lat, p.Lon)
	cl := http.Client{Timeout: 20 * time.Second}
	var out struct {
		Current struct {
			T float64 `json:"temperature_2m"`
			F float64 `json:"apparent_temperature"`
			C int     `json:"weather_code"`
			D int     `json:"is_day"`
		} `json:"current"`
	}
	resp, err := cl.Get(u)
	if err == nil {
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
	}
	mu.Lock()
	if err != nil || place != p {
		curErr = err != nil
	} else {
		curTemp, curFeels, curCode, curErr = out.Current.T, out.Current.F, out.Current.C, false
		curDay = out.Current.D == 1
	}
	failed := curErr
	mu.Unlock()
	pPostMessage.Call(hwndMain, wmRefresh, 0, 0)
	if failed { // reintenta pronto si no hubo conexión
		time.AfterFunc(2*time.Minute, func() { pPostMessage.Call(hwndMain, wmFetch, 0, 0) })
	}
}

// ---------- servidor local y ventana ----------

func serve(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if b, err := os.ReadFile(filepath.Join(dataDir, "index.html")); err == nil && len(b) > 1000 {
			w.Write(b) // versión actualizada de la página, si existe
			return
		}
		w.Write(page)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/x-icon")
		w.Write(favicon)
	})
	mux.HandleFunc("/api/place", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var p Place
			if json.NewDecoder(r.Body).Decode(&p) != nil || p.Name == "" {
				http.Error(w, "ciudad no válida", 400)
				return
			}
			mu.Lock()
			place, curTemp = p, math.NaN()
			mu.Unlock()
			savePlace(p)
			pPostMessage.Call(hwndMain, wmRefresh, 0, 0)
			pPostMessage.Call(hwndMain, wmFetch, 0, 0)
		}
		mu.Lock()
		p := place
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("/api/alto", func(w http.ResponseWriter, r *http.Request) {
		var h int
		if r.Method == http.MethodPost {
			fmt.Fscan(r.Body, &h)
			side := uintptr(0)
			if r.URL.Query().Get("p") == "lateral" {
				side = 1
			}
			pPostMessage.Call(hwndMain, wmPanelHeight, uintptr(h), side)
		}
	})
	mux.HandleFunc("/api/lateral", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			pPostMessage.Call(hwndMain, wmSideOpen, 0, 0)
		}
	})
	mux.HandleFunc("/api/ocultar", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			pPostMessage.Call(hwndMain, wmPanelHide, 0, 0)
		}
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			pPostMessage.Call(hwndMain, 0x0010 /*WM_CLOSE*/, 0, 0)
		}
	})
	http.Serve(ln, mux)
}

func launchBrowser(link string) {
	base := os.Getenv("LOCALAPPDATA")
	pf, pf86 := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")
	for _, b := range []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
	} {
		if _, err := os.Stat(b); err == nil {
			cmd := exec.Command(b, "--app="+link, "--user-data-dir="+filepath.Join(dataDir, "perfil"),
				"--window-size=500,900", "--no-first-run", "--no-default-browser-check")
			if cmd.Start() == nil {
				return
			}
		}
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", link).Start()
}

func openWindow() { go launchBrowser("http://" + addr + "/") }

func main() {
	runtime.LockOSThread()

	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	dataDir = filepath.Join(base, "TiempoTemuco")
	os.MkdirAll(dataDir, 0o755)

	// Una sola instancia: si ya está abierto, solo muestra la ventana.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		launchBrowser("http://" + addr + "/")
		return
	}
	loadPlace()
	go serve(ln)

	pSetDpiAwarenessCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	hInst, _, _ := pGetModuleHandle.Call(0)
	cls := u16("TiempoTemucoTray")
	wc := wndClassEx{lpfnWndProc: syscall.NewCallback(wndProc), hInstance: hInst, lpszClassName: cls}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	hwndMain, _, _ = pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("Tiempo Temuco"))),
		0, 0, 0, 0, 0, 0, 0, hInst, 0)
	if hwndMain == 0 {
		msgBox("No pude crear el ícono de la barra de tareas.")
		return
	}
	taskbarMsg, _, _ = pRegisterWindowMsg.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))

	// La primera vez se activa el inicio automático; después respeta lo que elija el usuario.
	flag := filepath.Join(dataDir, "inicio-configurado")
	if _, err := os.Stat(flag); err != nil {
		setStartup(true)
		os.WriteFile(flag, []byte("1"), 0o644)
	} else if startupEnabled() {
		setStartup(true) // mantiene la ruta al día si se movió el .exe
	}
	alInicio := false
	for _, a := range os.Args[1:] {
		if a == "--inicio" {
			alInicio = true
		}
	}

	old2 := notifyIconData{hWnd: hwndMain, uID: 2}
	old2.cbSize = uint32(unsafe.Sizeof(old2))
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&old2)))
	updateTray(true)
	time.AfterFunc(3*time.Second, promoteTrayIcon)
	go fetchWeather()
	pSetTimer.Call(hwndMain, 1, 10*60*1000, 0) // cada 10 minutos
	if !alInicio {
		showPanel()
	}

	var m msgT
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}
