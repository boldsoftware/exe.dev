// Theme management
class ThemeManager {
    constructor() {
        this.themes = {
            light: {
                background: '#ffffff',
                foreground: '#24292f',
                cursor: '#0969da',
                selectionBackground: '#0969da40',
                selectionForeground: '#24292f',
                black: '#24292f',
                red: '#cf222e',
                green: '#116329',
                yellow: '#4d2d00',
                blue: '#0969da',
                magenta: '#8250df',
                cyan: '#1b7c83',
                white: '#6e7681',
                brightBlack: '#656d76',
                brightRed: '#a40e26',
                brightGreen: '#1a7f37',
                brightYellow: '#633c01',
                brightBlue: '#218bff',
                brightMagenta: '#a475f9',
                brightCyan: '#3192aa',
                brightWhite: '#8c959f'
            },
            dark: {
                background: '#0d1117',
                foreground: '#e6edf3',
                cursor: '#58a6ff',
                selectionBackground: '#58a6ff4d',
                black: '#484f58',
                red: '#ff7b72',
                green: '#3fb950',
                yellow: '#d29922',
                blue: '#58a6ff',
                magenta: '#bc8cff',
                cyan: '#39c5cf',
                white: '#b1bac4',
                brightBlack: '#6e7681',
                brightRed: '#ffa198',
                brightGreen: '#56d364',
                brightYellow: '#e3b341',
                brightBlue: '#79c0ff',
                brightMagenta: '#d2a8ff',
                brightCyan: '#56d4dd',
                brightWhite: '#f0f6fc'
            }
        };
        // 'system', 'light', or 'dark'
        this.mode = this.loadMode();
        this.applyMode(this.mode, false);
    }

    loadMode() {
        const saved = localStorage.getItem('terminal-theme');
        if (saved === 'dark' || saved === 'light' || saved === 'system') {
            return saved;
        }
        return 'system';
    }

    saveMode(mode) {
        if (mode === 'system') {
            localStorage.removeItem('terminal-theme');
        } else {
            localStorage.setItem('terminal-theme', mode);
        }
    }

    getSystemTheme() {
        if (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) {
            return 'dark';
        }
        return 'light';
    }

    onSystemThemeChange(callback) {
        if (window.matchMedia) {
            window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
                if (this.mode === 'system') {
                    callback(this.getEffectiveTheme());
                }
            });
        }
    }

    applyMode(mode, save = true) {
        this.mode = mode;
        if (mode === 'system') {
            document.documentElement.removeAttribute('data-theme');
        } else {
            document.documentElement.setAttribute('data-theme', mode);
        }
        if (save) {
            this.saveMode(mode);
        }
    }

    setMode(mode) {
        if (mode === 'system' || mode === 'light' || mode === 'dark') {
            this.applyMode(mode);
        }
    }

    getMode() {
        return this.mode;
    }

    getEffectiveTheme() {
        if (this.mode === 'system') {
            return this.getSystemTheme();
        }
        return this.mode;
    }

    getTerminalTheme() {
        return this.themes[this.getEffectiveTheme()];
    }
}

