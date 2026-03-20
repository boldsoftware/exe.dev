package execore

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Board dimensions
const (
	boardWidth  = 10
	boardHeight = 20
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[96m"
	colorYellow = "\033[93m"
	colorPurple = "\033[95m"
	colorGreen  = "\033[92m"
	colorRed    = "\033[91m"
	colorBlue   = "\033[94m"
	colorOrange = "\033[33m"
	colorGray   = "\033[2m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"

	colorBgCyan   = "\033[106m"
	colorBgYellow = "\033[103m"
	colorBgPurple = "\033[105m"
	colorBgGreen  = "\033[102m"
	colorBgRed    = "\033[101m"
	colorBgBlue   = "\033[104m"
	colorBgOrange = "\033[43m"
	colorBgGray   = "\033[100m"
)

// Piece types
const (
	pieceNone = iota
	pieceI
	pieceO
	pieceT
	pieceS
	pieceZ
	pieceJ
	pieceL
)

// point represents a coordinate on the board.
type point struct {
	x, y int
}

// tetromino defines a piece with its rotations.
type tetromino struct {
	rotations [4][4]point // 4 rotation states, each with 4 blocks
	color     int
}

// All 7 tetrominoes with their rotation states.
// Coordinates are relative to a pivot point.
var tetrominoes = [7]tetromino{
	// I piece
	{
		rotations: [4][4]point{
			{{0, 1}, {1, 1}, {2, 1}, {3, 1}},
			{{2, 0}, {2, 1}, {2, 2}, {2, 3}},
			{{0, 2}, {1, 2}, {2, 2}, {3, 2}},
			{{1, 0}, {1, 1}, {1, 2}, {1, 3}},
		},
		color: pieceI,
	},
	// O piece
	{
		rotations: [4][4]point{
			{{0, 0}, {1, 0}, {0, 1}, {1, 1}},
			{{0, 0}, {1, 0}, {0, 1}, {1, 1}},
			{{0, 0}, {1, 0}, {0, 1}, {1, 1}},
			{{0, 0}, {1, 0}, {0, 1}, {1, 1}},
		},
		color: pieceO,
	},
	// T piece
	{
		rotations: [4][4]point{
			{{1, 0}, {0, 1}, {1, 1}, {2, 1}},
			{{1, 0}, {1, 1}, {2, 1}, {1, 2}},
			{{0, 1}, {1, 1}, {2, 1}, {1, 2}},
			{{1, 0}, {0, 1}, {1, 1}, {1, 2}},
		},
		color: pieceT,
	},
	// S piece
	{
		rotations: [4][4]point{
			{{1, 0}, {2, 0}, {0, 1}, {1, 1}},
			{{1, 0}, {1, 1}, {2, 1}, {2, 2}},
			{{1, 1}, {2, 1}, {0, 2}, {1, 2}},
			{{0, 0}, {0, 1}, {1, 1}, {1, 2}},
		},
		color: pieceS,
	},
	// Z piece
	{
		rotations: [4][4]point{
			{{0, 0}, {1, 0}, {1, 1}, {2, 1}},
			{{2, 0}, {1, 1}, {2, 1}, {1, 2}},
			{{0, 1}, {1, 1}, {1, 2}, {2, 2}},
			{{1, 0}, {0, 1}, {1, 1}, {0, 2}},
		},
		color: pieceZ,
	},
	// J piece
	{
		rotations: [4][4]point{
			{{0, 0}, {0, 1}, {1, 1}, {2, 1}},
			{{1, 0}, {2, 0}, {1, 1}, {1, 2}},
			{{0, 1}, {1, 1}, {2, 1}, {2, 2}},
			{{1, 0}, {1, 1}, {0, 2}, {1, 2}},
		},
		color: pieceJ,
	},
	// L piece
	{
		rotations: [4][4]point{
			{{2, 0}, {0, 1}, {1, 1}, {2, 1}},
			{{1, 0}, {1, 1}, {1, 2}, {2, 2}},
			{{0, 1}, {1, 1}, {2, 1}, {0, 2}},
			{{0, 0}, {1, 0}, {1, 1}, {1, 2}},
		},
		color: pieceL,
	},
}

// SRS wall kick data for J, L, S, T, Z pieces.
var wallKicksJLSTZ = [4][5]point{
	// 0->R
	{{0, 0}, {-1, 0}, {-1, -1}, {0, 2}, {-1, 2}},
	// R->2
	{{0, 0}, {1, 0}, {1, 1}, {0, -2}, {1, -2}},
	// 2->L
	{{0, 0}, {1, 0}, {1, -1}, {0, 2}, {1, 2}},
	// L->0
	{{0, 0}, {-1, 0}, {-1, 1}, {0, -2}, {-1, -2}},
}

// SRS wall kick data for I piece.
var wallKicksI = [4][5]point{
	// 0->R
	{{0, 0}, {-2, 0}, {1, 0}, {-2, 1}, {1, -2}},
	// R->2
	{{0, 0}, {-1, 0}, {2, 0}, {-1, -2}, {2, 1}},
	// 2->L
	{{0, 0}, {2, 0}, {-1, 0}, {2, -1}, {-1, 2}},
	// L->0
	{{0, 0}, {1, 0}, {-2, 0}, {1, 2}, {-2, -1}},
}

// activePiece tracks the current falling piece state.
type activePiece struct {
	typ      int // index into tetrominoes (0-6)
	rotation int // 0-3
	x, y     int // position offset on the board
}

// cells returns the absolute board coordinates of this piece's blocks.
func (p *activePiece) cells() [4]point {
	var cells [4]point
	blocks := tetrominoes[p.typ].rotations[p.rotation]
	for i, b := range blocks {
		cells[i] = point{p.x + b.x, p.y + b.y}
	}
	return cells
}

// tickMsg is sent on each gravity tick.
type tickMsg time.Time

// tetrisModel is the Bubble Tea model for the Tetris game.
type tetrisModel struct {
	board     [boardHeight][boardWidth]int // 0 = empty, 1-7 = piece color
	current   activePiece
	nextPiece int // index into tetrominoes
	holdPiece int // -1 = none, 0-6 = piece type
	holdUsed  bool

	bag    []int // 7-bag randomizer
	bagPos int
	rng    *rand.Rand

	score    int
	lines    int
	level    int
	gameOver bool

	width  int // terminal width
	height int // terminal height
}

// newTetrisModel creates and returns an initialized Tetris model.
func newTetrisModel(width, height int) *tetrisModel {
	m := &tetrisModel{
		holdPiece: -1,
		level:     1,
		width:     width,
		height:    height,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	m.fillBag()
	m.nextPiece = m.drawFromBag()
	m.spawnPiece()
	return m
}

// fillBag generates a new shuffled bag of all 7 piece types.
func (m *tetrisModel) fillBag() {
	m.bag = []int{0, 1, 2, 3, 4, 5, 6}
	for i := len(m.bag) - 1; i > 0; i-- {
		j := m.rng.Intn(i + 1)
		m.bag[i], m.bag[j] = m.bag[j], m.bag[i]
	}
	m.bagPos = 0
}

// drawFromBag returns the next piece type from the bag, refilling if needed.
func (m *tetrisModel) drawFromBag() int {
	if m.bagPos >= len(m.bag) {
		m.fillBag()
	}
	p := m.bag[m.bagPos]
	m.bagPos++
	return p
}

// spawnPiece spawns a new piece at the top of the board.
func (m *tetrisModel) spawnPiece() {
	m.current = activePiece{
		typ:      m.nextPiece,
		rotation: 0,
		x:        3,
		y:        0,
	}
	m.nextPiece = m.drawFromBag()
	m.holdUsed = false

	// Check if spawn position is valid
	if !m.isValid(m.current) {
		m.gameOver = true
	}
}

// isValid checks if a piece position is legal (in bounds and no overlap).
func (m *tetrisModel) isValid(p activePiece) bool {
	cells := p.cells()
	for _, c := range cells {
		if c.x < 0 || c.x >= boardWidth || c.y < 0 || c.y >= boardHeight {
			return false
		}
		if m.board[c.y][c.x] != 0 {
			return false
		}
	}
	return true
}

// lockPiece locks the current piece onto the board.
func (m *tetrisModel) lockPiece() {
	color := tetrominoes[m.current.typ].color
	cells := m.current.cells()
	for _, c := range cells {
		if c.y >= 0 && c.y < boardHeight && c.x >= 0 && c.x < boardWidth {
			m.board[c.y][c.x] = color
		}
	}
}

// clearLines checks and clears completed lines, returning the count.
func (m *tetrisModel) clearLines() int {
	cleared := 0
	for y := boardHeight - 1; y >= 0; y-- {
		full := true
		for x := 0; x < boardWidth; x++ {
			if m.board[y][x] == 0 {
				full = false
				break
			}
		}
		if full {
			cleared++
			// Shift everything above down
			for yy := y; yy > 0; yy-- {
				m.board[yy] = m.board[yy-1]
			}
			// Clear top row
			for x := 0; x < boardWidth; x++ {
				m.board[0][x] = 0
			}
			y++ // Re-check this row since we shifted
		}
	}
	return cleared
}

// addScore updates score based on lines cleared.
func (m *tetrisModel) addScore(linesCleared int) {
	points := 0
	switch linesCleared {
	case 1:
		points = 100
	case 2:
		points = 300
	case 3:
		points = 500
	case 4:
		points = 800
	}
	m.score += points * m.level
	m.lines += linesCleared
	m.level = m.lines/10 + 1
}

// gravityInterval returns the tick duration based on the current level.
func (m *tetrisModel) gravityInterval() time.Duration {
	// Start at 1000ms, decrease by ~60ms per level, minimum 50ms
	ms := 1000 - (m.level-1)*60
	if ms < 50 {
		ms = 50
	}
	return time.Duration(ms) * time.Millisecond
}

// moveLeft moves the piece left if possible.
func (m *tetrisModel) moveLeft() {
	try := m.current
	try.x--
	if m.isValid(try) {
		m.current = try
	}
}

// moveRight moves the piece right if possible.
func (m *tetrisModel) moveRight() {
	try := m.current
	try.x++
	if m.isValid(try) {
		m.current = try
	}
}

// moveDown moves the piece down. Returns true if it moved, false if locked.
func (m *tetrisModel) moveDown() bool {
	try := m.current
	try.y++
	if m.isValid(try) {
		m.current = try
		return true
	}
	return false
}

// rotateCW rotates the piece clockwise with SRS wall kicks.
func (m *tetrisModel) rotateCW() {
	try := m.current
	newRot := (try.rotation + 1) % 4
	try.rotation = newRot

	// Choose the appropriate wall kick table
	var kicks [5]point
	if m.current.typ == 0 { // I piece
		kicks = wallKicksI[m.current.rotation]
	} else {
		kicks = wallKicksJLSTZ[m.current.rotation]
	}

	for _, kick := range kicks {
		candidate := try
		candidate.x += kick.x
		candidate.y -= kick.y // SRS uses Y-up, our board is Y-down
		if m.isValid(candidate) {
			m.current = candidate
			return
		}
	}
}

// hardDrop drops the piece to the lowest valid position and locks it.
func (m *tetrisModel) hardDrop() {
	dropDist := 0
	for {
		try := m.current
		try.y++
		if m.isValid(try) {
			m.current = try
			dropDist++
		} else {
			break
		}
	}
	// Award points for hard drop (2 points per cell dropped)
	m.score += dropDist * 2
	m.lockPiece()
	cleared := m.clearLines()
	m.addScore(cleared)
	m.spawnPiece()
}

// ghostY returns the Y position where the ghost piece would land.
func (m *tetrisModel) ghostY() int {
	ghost := m.current
	for {
		try := ghost
		try.y++
		if m.isValid(try) {
			ghost = try
		} else {
			break
		}
	}
	return ghost.y
}

// holdSwap swaps the current piece with the hold piece.
func (m *tetrisModel) holdSwap() {
	if m.holdUsed {
		return
	}
	m.holdUsed = true

	if m.holdPiece == -1 {
		m.holdPiece = m.current.typ
		m.spawnPiece()
	} else {
		old := m.holdPiece
		m.holdPiece = m.current.typ
		m.current = activePiece{
			typ:      old,
			rotation: 0,
			x:        3,
			y:        0,
		}
		if !m.isValid(m.current) {
			m.gameOver = true
		}
	}
}

// restart resets the game state.
func (m *tetrisModel) restart() {
	for y := 0; y < boardHeight; y++ {
		for x := 0; x < boardWidth; x++ {
			m.board[y][x] = 0
		}
	}
	m.score = 0
	m.lines = 0
	m.level = 1
	m.gameOver = false
	m.holdPiece = -1
	m.holdUsed = false
	m.fillBag()
	m.nextPiece = m.drawFromBag()
	m.spawnPiece()
}

// colorForPiece returns the ANSI color string for a piece type.
func colorForPiece(pieceType int) string {
	switch pieceType {
	case pieceI:
		return colorCyan
	case pieceO:
		return colorYellow
	case pieceT:
		return colorPurple
	case pieceS:
		return colorGreen
	case pieceZ:
		return colorRed
	case pieceJ:
		return colorBlue
	case pieceL:
		return colorOrange
	default:
		return colorReset
	}
}

// bgColorForPiece returns the ANSI background color string for a piece type.
func bgColorForPiece(pieceType int) string {
	switch pieceType {
	case pieceI:
		return colorBgCyan
	case pieceO:
		return colorBgYellow
	case pieceT:
		return colorBgPurple
	case pieceS:
		return colorBgGreen
	case pieceZ:
		return colorBgRed
	case pieceJ:
		return colorBgBlue
	case pieceL:
		return colorBgOrange
	default:
		return ""
	}
}

// Init implements tea.Model.
func (m *tetrisModel) Init() tea.Cmd {
	return tea.Tick(m.gravityInterval(), func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update implements tea.Model.
func (m *tetrisModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit

		case "r":
			if m.gameOver {
				m.restart()
				return m, tea.Tick(m.gravityInterval(), func(t time.Time) tea.Msg {
					return tickMsg(t)
				})
			}
			return m, nil
		}

		// Game controls only work when not game over
		if m.gameOver {
			return m, nil
		}

		switch msg.String() {
		case "left", "a":
			m.moveLeft()
		case "right", "d":
			m.moveRight()
		case "down", "s":
			if !m.moveDown() {
				m.lockPiece()
				cleared := m.clearLines()
				m.addScore(cleared)
				m.spawnPiece()
			} else {
				m.score++ // Soft drop bonus
			}
		case "up", "w":
			m.rotateCW()
		case " ":
			m.hardDrop()
		case "c", "shift":
			m.holdSwap()
		}
		return m, nil

	case tickMsg:
		if m.gameOver {
			return m, nil
		}
		if !m.moveDown() {
			m.lockPiece()
			cleared := m.clearLines()
			m.addScore(cleared)
			m.spawnPiece()
		}
		return m, tea.Tick(m.gravityInterval(), func(t time.Time) tea.Msg {
			return tickMsg(t)
		})
	}

	return m, nil
}

// View implements tea.Model.
func (m *tetrisModel) View() string {
	var b strings.Builder

	// Build a composite board with the current piece and ghost overlaid
	type cellInfo struct {
		color int
		ghost bool
	}
	var display [boardHeight][boardWidth]cellInfo

	// Copy locked cells
	for y := 0; y < boardHeight; y++ {
		for x := 0; x < boardWidth; x++ {
			display[y][x] = cellInfo{color: m.board[y][x]}
		}
	}

	if !m.gameOver {
		// Draw ghost piece
		ghostY := m.ghostY()
		if ghostY != m.current.y {
			ghost := m.current
			ghost.y = ghostY
			ghostCells := ghost.cells()
			for _, c := range ghostCells {
				if c.y >= 0 && c.y < boardHeight && c.x >= 0 && c.x < boardWidth {
					if display[c.y][c.x].color == 0 {
						display[c.y][c.x] = cellInfo{
							color: tetrominoes[m.current.typ].color,
							ghost: true,
						}
					}
				}
			}
		}

		// Draw current piece
		cells := m.current.cells()
		for _, c := range cells {
			if c.y >= 0 && c.y < boardHeight && c.x >= 0 && c.x < boardWidth {
				display[c.y][c.x] = cellInfo{color: tetrominoes[m.current.typ].color}
			}
		}
	}

	// Prepare right-side panel lines
	var panel []string

	// Hold piece
	panel = append(panel, fmt.Sprintf("%s%s HOLD %s", colorBold, colorBold, colorReset))
	panel = append(panel, renderMiniPiece(m.holdPiece)...)
	panel = append(panel, "")

	// Next piece
	panel = append(panel, fmt.Sprintf("%s%s NEXT %s", colorBold, colorBold, colorReset))
	panel = append(panel, renderMiniPiece(m.nextPiece)...)
	panel = append(panel, "")

	// Score / Level / Lines
	panel = append(panel, fmt.Sprintf("%s%s SCORE %s", colorBold, colorBold, colorReset))
	panel = append(panel, fmt.Sprintf(" %s%d%s", colorBold, m.score, colorReset))
	panel = append(panel, "")
	panel = append(panel, fmt.Sprintf("%s%s LEVEL %s", colorBold, colorBold, colorReset))
	panel = append(panel, fmt.Sprintf(" %s%d%s", colorBold, m.level, colorReset))
	panel = append(panel, "")
	panel = append(panel, fmt.Sprintf("%s%s LINES %s", colorBold, colorBold, colorReset))
	panel = append(panel, fmt.Sprintf(" %s%d%s", colorBold, m.lines, colorReset))
	panel = append(panel, "")

	// Controls
	panel = append(panel, fmt.Sprintf("%s CONTROLS %s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s ←→  Move%s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s ↑    Rotate%s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s ↓    Soft Drop%s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s Space Hard Drop%s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s c    Hold%s", colorGray, colorReset))
	panel = append(panel, fmt.Sprintf("%s q    Quit%s", colorGray, colorReset))

	// Title
	b.WriteString("\r\n")
	b.WriteString(fmt.Sprintf("  %s%s ╔══ T E T R I S ══╗ %s\r\n", colorBold, colorCyan, colorReset))

	// Top border of board
	b.WriteString(fmt.Sprintf("  %s╔════════════════════╗%s", colorBold, colorReset))
	if len(panel) > 0 {
		b.WriteString("  ")
	}
	b.WriteString("\r\n")

	// Board rows
	for y := 0; y < boardHeight; y++ {
		b.WriteString(fmt.Sprintf("  %s║%s", colorBold, colorReset))
		for x := 0; x < boardWidth; x++ {
			cell := display[y][x]
			if cell.color != 0 && cell.ghost {
				// Ghost piece - dim colored dots
				b.WriteString(fmt.Sprintf("%s%s░░%s", colorDim, colorForPiece(cell.color), colorReset))
			} else if cell.color != 0 {
				// Filled cell with background color
				b.WriteString(fmt.Sprintf("%s%s  %s", bgColorForPiece(cell.color), colorForPiece(cell.color), colorReset))
			} else {
				// Empty cell
				b.WriteString(fmt.Sprintf("%s· %s", colorGray, colorReset))
			}
		}
		b.WriteString(fmt.Sprintf("%s║%s", colorBold, colorReset))

		// Right panel
		panelIdx := y
		if panelIdx < len(panel) {
			b.WriteString("  " + panel[panelIdx])
		}

		b.WriteString("\r\n")
	}

	// Bottom border
	b.WriteString(fmt.Sprintf("  %s╚════════════════════╝%s", colorBold, colorReset))
	b.WriteString("\r\n")

	// Game over message
	if m.gameOver {
		b.WriteString("\r\n")
		b.WriteString(fmt.Sprintf("  %s%s  ══ GAME OVER ══%s\r\n", colorBold, colorRed, colorReset))
		b.WriteString(fmt.Sprintf("  %s  Score: %d%s\r\n", colorBold, m.score, colorReset))
		b.WriteString(fmt.Sprintf("  %s  Press 'r' to restart%s\r\n", colorGray, colorReset))
		b.WriteString(fmt.Sprintf("  %s  Press 'q' to quit%s\r\n", colorGray, colorReset))
	}

	b.WriteString("\r\n")

	return b.String()
}

// renderMiniPiece renders a 4x2 mini preview of a piece type.
// Returns a slice of display lines.
func renderMiniPiece(pieceType int) []string {
	if pieceType < 0 {
		return []string{
			fmt.Sprintf(" %s--------%s", colorGray, colorReset),
			fmt.Sprintf(" %s        %s", colorGray, colorReset),
		}
	}

	// Build a small 4x4 grid for the piece in rotation 0
	var grid [4][4]bool
	blocks := tetrominoes[pieceType].rotations[0]
	for _, b := range blocks {
		if b.x < 4 && b.y < 4 {
			grid[b.y][b.x] = true
		}
	}

	color := colorForPiece(tetrominoes[pieceType].color)
	bg := bgColorForPiece(tetrominoes[pieceType].color)

	var lines []string
	for y := 0; y < 2; y++ {
		var row strings.Builder
		row.WriteString(" ")
		for x := 0; x < 4; x++ {
			if grid[y][x] {
				row.WriteString(fmt.Sprintf("%s%s  %s", bg, color, colorReset))
			} else {
				row.WriteString("  ")
			}
		}
		lines = append(lines, row.String())
	}
	return lines
}
