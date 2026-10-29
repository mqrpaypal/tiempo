// Dibujos del estado del cielo para la bandeja (sol, luna, nubes, lluvia, nieve, tormenta, neblina).
// Se dibujan con formas simples y suavizado propio, así se ven nítidos en cualquier tamaño de ícono.
package main

import "math"

type rgb struct{ r, g, b float64 }

type canvas struct {
	n          int
	r, g, b, a []float64
}

func newCanvas(n int) *canvas {
	s := n * n
	return &canvas{n: n, r: make([]float64, s), g: make([]float64, s), b: make([]float64, s), a: make([]float64, s)}
}

type shape func(x, y float64) bool

// fill pinta la forma (coordenadas de 0 a 1) con suavizado 4x4.
func (c *canvas) fill(in shape, col rgb) {
	const ss = 4
	n := float64(c.n)
	for py := 0; py < c.n; py++ {
		for px := 0; px < c.n; px++ {
			hit := 0
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					x := (float64(px) + (float64(i)+0.5)/ss) / n
					y := (float64(py) + (float64(j)+0.5)/ss) / n
					if in(x, y) {
						hit++
					}
				}
			}
			if hit == 0 {
				continue
			}
			sa := float64(hit) / (ss * ss)
			k := py*c.n + px
			da := c.a[k]
			oa := sa + da*(1-sa)
			c.r[k] = (col.r*sa + c.r[k]*da*(1-sa)) / oa
			c.g[k] = (col.g*sa + c.g[k]*da*(1-sa)) / oa
			c.b[k] = (col.b*sa + c.b[k]*da*(1-sa)) / oa
			c.a[k] = oa
		}
	}
}

// bgra devuelve los píxeles en el formato que usa Windows para íconos (BGRA, alfa directo).
func (c *canvas) bgra() []byte {
	out := make([]byte, c.n*c.n*4)
	for k := range c.a {
		out[k*4+0] = byte(math.Round(c.b[k] * 255))
		out[k*4+1] = byte(math.Round(c.g[k] * 255))
		out[k*4+2] = byte(math.Round(c.r[k] * 255))
		out[k*4+3] = byte(math.Round(c.a[k] * 255))
	}
	return out
}

func circle(cx, cy, r float64) shape {
	return func(x, y float64) bool { return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r }
}

// capsule: segmento con extremos redondeados.
func capsule(x1, y1, x2, y2, r float64) shape {
	return func(x, y float64) bool {
		dx, dy := x2-x1, y2-y1
		t := ((x-x1)*dx + (y-y1)*dy) / (dx*dx + dy*dy)
		t = math.Max(0, math.Min(1, t))
		qx, qy := x1+t*dx-x, y1+t*dy-y
		return qx*qx+qy*qy <= r*r
	}
}

func union(s ...shape) shape {
	return func(x, y float64) bool {
		for _, f := range s {
			if f(x, y) {
				return true
			}
		}
		return false
	}
}

func minus(a, b shape) shape { return func(x, y float64) bool { return a(x, y) && !b(x, y) } }

func hex(v uint32) rgb {
	return rgb{float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255}
}

// Nube dentro del cuadro (ox, oy) de tamaño s.
func cloudShape(ox, oy, s float64) shape {
	return union(
		circle(ox+0.30*s, oy+0.60*s, 0.19*s),
		circle(ox+0.53*s, oy+0.47*s, 0.25*s),
		circle(ox+0.75*s, oy+0.62*s, 0.17*s),
		capsule(ox+0.30*s, oy+0.66*s, ox+0.75*s, oy+0.66*s, 0.13*s),
	)
}

func sunShapes(cx, cy, r float64) (disc, rays shape) {
	var rs []shape
	for i := 0; i < 8; i++ {
		a := float64(i) * math.Pi / 4
		c, s := math.Cos(a), math.Sin(a)
		rs = append(rs, capsule(cx+c*r*1.5, cy+s*r*1.5, cx+c*r*2.05, cy+s*r*2.05, r*0.17))
	}
	return circle(cx, cy, r), union(rs...)
}

func moonShape(cx, cy, r float64) shape {
	return minus(circle(cx, cy, r), circle(cx+r*0.55, cy-r*0.35, r*0.85))
}

// drawWeather dibuja el ícono para un código de Open-Meteo.
// day: si es de día; light: si la barra de tareas usa tema claro.
func drawWeather(n, code int, day, light bool) []byte {
	c := newCanvas(n)
	drawWeatherOn(c, code, day, light, 0, 0, 1)
	return c.bgra()
}

// drawWeatherOn dibuja el estado del cielo dentro del cuadro (ox, oy, sz) del lienzo,
// en coordenadas de 0 a 1 del ícono completo.
func drawWeatherOn(cv *canvas, code int, day, light bool, ox, oy, sz float64) {
	c := &region{cv: cv, ox: ox, oy: oy, sz: sz}

	sunCol := hex(0xFFC233)
	moonCol := hex(0xF2D675)
	cloudCol := hex(0xE8EDF2)
	backCloud := hex(0xA9B4BF)
	fogCol := hex(0xC9D1D9)
	rainCol := hex(0x4DA8FF)
	snowCol := hex(0xCFEAFF)
	boltCol := hex(0xFFD43B)
	if light {
		moonCol = hex(0xC9A227)
		cloudCol = hex(0x8F9BA8)
		backCloud = hex(0x5F6B78)
		fogCol = hex(0x7D8894)
		rainCol = hex(0x1F78D1)
		snowCol = hex(0x4A90C8)
		boltCol = hex(0xE0A800)
	}

	celestial := func(cx, cy, r float64) {
		if day {
			disc, rays := sunShapes(cx, cy, r)
			c.fill(rays, sunCol)
			c.fill(disc, sunCol)
		} else {
			c.fill(moonShape(cx, cy, r*1.45), moonCol)
		}
	}
	drops := func(snow bool, count int) {
		xs := []float64{0.34, 0.54, 0.74}
		if count == 2 {
			xs = []float64{0.42, 0.66}
		}
		for _, x := range xs {
			if snow {
				c.fill(circle(x-0.03, 0.82, 0.075), snowCol)
			} else {
				c.fill(capsule(x, 0.70, x-0.07, 0.90, 0.055), rainCol)
			}
		}
	}
	topCloud := func(col rgb) { c.fill(cloudShape(0.02, -0.14, 0.96), col) }

	switch {
	case code == 0: // despejado
		celestial(0.5, 0.5, 0.21)
	case code == 1 || code == 2: // parcialmente nublado
		celestial(0.36, 0.36, 0.15)
		c.fill(cloudShape(0.14, 0.22, 0.86), cloudCol)
	case code == 3: // nublado
		c.fill(cloudShape(0.22, -0.02, 0.78), backCloud)
		c.fill(cloudShape(0.0, 0.16, 0.92), cloudCol)
	case code == 45 || code == 48: // neblina
		c.fill(capsule(0.14, 0.32, 0.80, 0.32, 0.065), fogCol)
		c.fill(capsule(0.22, 0.52, 0.88, 0.52, 0.065), fogCol)
		c.fill(capsule(0.12, 0.72, 0.74, 0.72, 0.065), fogCol)
	case code >= 51 && code <= 57: // llovizna
		topCloud(cloudCol)
		drops(false, 2)
	case (code >= 61 && code <= 67) || (code >= 80 && code <= 82): // lluvia y chubascos
		topCloud(cloudCol)
		drops(false, 3)
	case (code >= 71 && code <= 77) || code == 85 || code == 86: // nieve
		topCloud(cloudCol)
		drops(true, 3)
	case code >= 95: // tormenta
		topCloud(backCloud)
		c.fill(union(
			capsule(0.56, 0.60, 0.44, 0.78, 0.05),
			capsule(0.44, 0.78, 0.60, 0.78, 0.05),
			capsule(0.60, 0.78, 0.48, 0.96, 0.05),
		), boltCol)
	default: // sin datos todavía
		c.fill(cloudShape(0.0, 0.08, 1.0), cloudCol)
	}
}

// region dibuja formas dentro de un cuadro del lienzo.
type region struct {
	cv         *canvas
	ox, oy, sz float64
}

func (r *region) fill(s shape, col rgb) {
	r.cv.fill(func(x, y float64) bool { return s((x-r.ox)/r.sz, (y-r.oy)/r.sz) }, col)
}

// fillMask pinta un color usando una máscara de cobertura (0 a 1) por píxel.
func (c *canvas) fillMask(mask []float64, col rgb) {
	for k, sa := range mask {
		if sa <= 0 {
			continue
		}
		if sa > 1 {
			sa = 1
		}
		da := c.a[k]
		oa := sa + da*(1-sa)
		c.r[k] = (col.r*sa + c.r[k]*da*(1-sa)) / oa
		c.g[k] = (col.g*sa + c.g[k]*da*(1-sa)) / oa
		c.b[k] = (col.b*sa + c.b[k]*da*(1-sa)) / oa
		c.a[k] = oa
	}
}

// dilate agranda una máscara (para el contorno que separa el número del dibujo).
func dilate(mask []float64, n int, rad float64) []float64 {
	out := make([]float64, len(mask))
	ri := int(math.Ceil(rad))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			best := 0.0
			for dy := -ri; dy <= ri; dy++ {
				for dx := -ri; dx <= ri; dx++ {
					xx, yy := x+dx, y+dy
					if xx < 0 || yy < 0 || xx >= n || yy >= n {
						continue
					}
					d := math.Hypot(float64(dx), float64(dy))
					f := math.Max(0, math.Min(1, rad+0.5-d))
					if v := mask[yy*n+xx] * f; v > best {
						best = v
					}
				}
			}
			out[y*n+x] = best
		}
	}
	return out
}

// ringCoverage: cuánto cubre un anillo (para el símbolo °) el píxel (px, py).
func ringCoverage(px, py int, cx, cy, r, th float64) float64 {
	const ss = 4
	hit := 0
	for j := 0; j < ss; j++ {
		for i := 0; i < ss; i++ {
			x := float64(px) + (float64(i)+0.5)/ss
			y := float64(py) + (float64(j)+0.5)/ss
			d := math.Hypot(x-cx, y-cy) - r
			if math.Abs(d) <= th/2 {
				hit++
			}
		}
	}
	return float64(hit) / (ss * ss)
}
