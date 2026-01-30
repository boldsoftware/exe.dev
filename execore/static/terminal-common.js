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
        this.currentTheme = this.loadTheme();
        this.applyTheme(this.currentTheme);
    }

    loadTheme() {
        const saved = localStorage.getItem('terminal-theme');
        if (saved === 'dark' || saved === 'light') {
            return saved;
        }
        // Default to system preference, or dark if no preference
        if (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) {
            return 'light';
        }
        return 'dark';
    }

    saveTheme(theme) {
        localStorage.setItem('terminal-theme', theme);
    }

    applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        this.currentTheme = theme;
        this.saveTheme(theme);
    }

    toggle() {
        const newTheme = this.currentTheme === 'light' ? 'dark' : 'light';
        this.applyTheme(newTheme);
        return newTheme;
    }

    getTerminalTheme() {
        return this.themes[this.currentTheme];
    }
}

