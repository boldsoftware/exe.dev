package boxname

import (
	mathrand "math/rand"
	"strings"
)

// wordEmojis maps select words from the name vocabulary to a fitting emoji.
// Not every word needs an entry; callers fall back to FallbackEmoji() for words
// without a mapping (and for names that weren't auto-generated).
var wordEmojis = map[string]string{
	// NATO phonetic + military
	"alpha": "🅰️", "bravo": "👏", "charlie": "🕺", "delta": "🔺", "echo": "📣",
	"foxtrot": "🦊", "golf": "⛳", "hotel": "🏨", "india": "🇮🇳", "juliet": "💃",
	"kilo": "⚖️", "lima": "🫘", "mike": "🎤", "november": "🍂", "oscar": "🏆",
	"papa": "👴", "quebec": "🍁", "romeo": "🌹", "sierra": "🏔️", "tango": "💃",
	"uniform": "🎽", "victor": "🏅", "whiskey": "🥃", "xray": "🩻", "yankee": "🧢",
	"zulu": "🛡️",

	// WWII / older phonetics
	"able": "💪", "baker": "👨\u200d🍳", "dog": "🐕", "early": "🌅", "waltz": "🕺",
	"george": "🧑", "how": "❓", "item": "📦", "king": "👑",
	"love": "❤️", "nan": "🍞", "oboe": "🎶", "prep": "🧑\u200d🍳", "queen": "👸",
	"roger": "👍", "sweet": "🍬", "tare": "⚖️", "uncle": "👨", "victory": "✌️",
	"william": "🎩", "extra": "➕", "yolk": "🥚", "zebra": "🦓",

	// Nature & elements
	"earth": "🌍", "wind": "💨", "fire": "🔥", "water": "💧", "stone": "🪨",
	"tree": "🌳", "river": "🏞️", "mountain": "⛰️", "cloud": "☁️", "storm": "⛈️",
	"rain": "🌧️", "snow": "❄️", "ice": "🧊", "sun": "☀️", "moon": "🌙",
	"star": "⭐", "comet": "☄️", "nova": "💥", "eclipse": "🌑", "ocean": "🌊",
	"tide": "🌊", "sky": "🌤️", "oak": "🌳", "maple": "🍁", "pine": "🌲",
	"cedar": "🌲", "willow": "🌿", "elm": "🌳", "spruce": "🌲", "fir": "🌲",
	"breeze": "🍃", "mist": "🌫️", "frost": "🥶", "aurora": "🌌", "halo": "😇",
	"zenith": "🌟", "meadow": "🌾", "prairie": "🌾",

	// Animals
	"lion": "🦁", "tiger": "🐯", "bear": "🐻", "wolf": "🐺", "eagle": "🦅",
	"hawk": "🪶", "falcon": "🦅", "owl": "🦉", "otter": "🦦", "seal": "🦭",
	"whale": "🐋", "shark": "🦈", "orca": "🐳", "salmon": "🐟", "trout": "🐟",
	"crane": "🕊️", "heron": "🕊️", "sparrow": "🐦", "crow": "🐦\u200d⬛", "raven": "🐦\u200d⬛",
	"fox": "🦊", "badger": "🦡", "ferret": "🦡", "bird": "🐦", "bobcat": "🐈",
	"cougar": "🐆", "panther": "🐆", "cobra": "🐍", "viper": "🐍", "python": "🐍",
	"gecko":   "🦎",
	"penguin": "🐧", "dolphin": "🐬", "walrus": "🦭", "hedgehog": "🦔", "koala": "🐨",
	"panda": "🐼", "kangaroo": "🦘", "llama": "🦙", "alpaca": "🦙", "capybara": "🦫",
	"axolotl": "🦎", "narwhal": "🐋", "manatee": "🐋", "lemur": "🐒",
	"wombat": "🐾", "quokka": "🐾", "pelican": "🪶", "platypus": "🦆", "chinchilla": "🐭",
	"tortoise": "🐢", "lizard": "🦎",

	// Plants & garden
	"fern": "🌿", "moss": "🌿", "daisy": "🌼", "tulip": "🌷", "lotus": "🪷",
	"bamboo": "🎋", "clover": "🍀", "sunflower": "🌻",

	// Colors
	"red": "🟥", "blue": "🟦", "green": "🟩", "yellow": "🟨", "purple": "🟪",
	"violet": "🟣", "indigo": "🔵", "orange": "🟧", "egg": "🥚", "ruby": "💎",
	"gray": "⬜", "silver": "🥈", "gold": "🥇", "bronze": "🥉", "scarlet": "🔴",
	"crimson": "🟥", "azure": "🔷", "emerald": "💚", "jade": "💚", "amber": "🟠",

	// Space & science
	"asteroid": "☄️", "nebula": "🌌", "quasar": "✨", "galaxy": "🌌", "pulsar": "📡",
	"orbit": "🛰️", "photon": "💡", "quantum": "⚛️", "fusion": "☢️", "plasma": "🔥",
	"tin": "🥫", "quark": "⚛️", "meteor": "☄️", "cosmos": "🌌", "ion": "⚛️",
	"neutron": "⚛️", "proton": "⚛️", "electron": "⚛️",

	// Tools, tech & retro computing
	"format": "💾", "disk": "💽", "edit": "📝", "finder": "🔍", "paint": "🎨",
	"minesweeper": "💣", "fortune": "🔮", "lynx": "🐈", "telnet": "📞", "gopher": "🐿️",
	"ping": "📡", "traceroute": "🗺️", "router": "📶", "switch": "🔀", "ethernet": "🔌",
	"socket": "🔌", "kernel": "🌽", "patch": "🩹", "compile": "⚙️", "linker": "🔗",
	"loader": "📥", "buffer": "🧽", "cache": "🗄️", "cookie": "🍪", "daemon": "👻",
	"popcorn": "🍿", "driver": "🧑\u200d🚀",

	// Random objects / geography
	"anchor": "⚓", "beacon": "🚨", "bridge": "🌉", "compass": "🧭", "harbor": "⚓",
	"island": "🏝️", "lagoon": "🏞️", "mesa": "🏜️", "valley": "🏞️", "desert": "🏜️",
	"canyon": "🏜️", "fun": "🎉", "reef": "🐠", "stream": "💦", "dune": "🏜️",
	"grove": "🌳", "peak": "🏔️", "ridge": "⛰️", "plateau": "🗻",

	// Snacks, instruments & whimsy
	"yolo": "🎉", "parachute": "🪂", "waffle": "🧇", "noodle": "🍜",
	"pretzel": "🥨", "muffin": "🧁", "bagel": "🥯", "cupcake": "🧁", "popsicle": "🍦",
	"sundae": "🍨", "mango": "🥭", "kiwi": "🥝",
	"pancake": "🥞", "donut": "🍩", "scone": "🥐", "croissant": "🥐", "biscuit": "🍪",
	"dumpling": "🥟", "ramen": "🍜", "macaron": "🍪",
	"guitar": "🎸", "bass": "🎸", "piano": "🎹", "drum": "🥁", "flute": "🎶",
	"violin": "🎻", "cello": "🎻", "saxophone": "🎷", "trumpet": "🎺",
	"clarinet": "🎶", "bagpipe": "🎶", "xylophone": "🎶", "tambourine": "🪘",
	"piccolo": "🎶", "bugle": "📯", "fife": "🎶", "marimba": "🎶",
	"ukulele": "🎸", "harmonica": "🎶", "mandolin": "🎻", "accordion": "🪗", "tuba": "🎺",
	"kazoo": "🎶", "banjo": "🪕", "ocarina": "🎶",
	"balloon": "🎈", "kite": "🪁", "frisbee": "🥏", "yoyo": "🪀", "igloo": "🛖",
	"crayon": "🖍️", "jetpack": "🚀",
	"bubble": "🫧", "sparkle": "✨", "ripple": "🌊",
	"confetti": "🎊", "lantern": "🏮",

	// Misc fun
	"sphinx": "🦁", "obelisk": "🗿", "party": "🎉", "griffin": "🦅", "hydra": "🐉",
	"kraken": "🐙", "unicorn": "🦄", "pegasus": "🐎", "chimera": "🐐", "golem": "🗿",
	"spin": "🌀", "road": "🛣️", "alley": "🛤️", "sprite": "🧚", "fairy": "🧚",
	"dragon": "🐉", "wyvern": "🐉", "cyclops": "👁️", "satyr": "🐐", "noon": "🌞",
	"centaur": "🐎", "minotaur": "🐂", "harp": "🎵", "basilisk": "🐍", "leviathan": "🐉",
	"phoenix": "🔥", "yeti": "🧊", "valkyrie": "🛡️", "kelpie": "🐎",
}

// fallbackEmojis is a safe, non-political, SFW list to pick from when no word-based
// match exists and the LLM isn't available.
var fallbackEmojis = []string{
	"🐙", "🐳", "🦑", "🐠", "🐡", "🦀", "🐢", "🐬", "🦭", "🐟",
	"🦊", "🦝", "🐼", "🐨", "🐻", "🦁", "🐯", "🦓", "🦒", "🐘",
	"🦔", "🦇", "🐿️", "🦘", "🦦", "🦃", "🦚", "🦜", "🐧", "🦩",
	"🌵", "🌴", "🌲", "🌳", "🍀", "🌻", "🌷", "🌹", "🌸", "🪷",
	"🍄", "🌈", "⛅", "🌊", "🔥", "❄️", "⚡", "☄️", "🌙", "⭐",
	"🍎", "🍊", "🍋", "🍉", "🍇", "🍓", "🫐", "🍍", "🥝", "🥥",
	"🍕", "🍔", "🌮", "🍣", "🍪", "🍰", "🍩", "🍦", "🧁", "🥐",
	"⚽", "🏀", "🏈", "⚾", "🎾", "🏐", "🎱", "🏓", "🏸", "🥌",
	"🎨", "🎭", "🎬", "🎸", "🎹", "🎺", "🎻", "🥁", "🎤", "🎧",
	"🚀", "🛸", "🛰️", "🗿", "🏰", "🗼", "🎡", "🎢", "🎠", "🏯",
	"🧭", "🧲", "🔮", "💎", "🔔", "📯", "🎁", "🎈", "🎉", "🧩",
	"🦄", "🐉", "🧚", "🧙", "🧞", "🦕", "🦖", "🐲", "👾", "🤖",
}

// EmojiForName returns (emoji, true) if any word in the name has a known
// emoji mapping, choosing randomly among matches (excluding anything in
// avoid). Returns ("", false) if no mapping is found.
//
// avoid may be nil. Matching is case-insensitive.
func EmojiForName(name string, avoid map[string]bool) (string, bool) {
	lower := strings.ToLower(name)
	var candidates []string
	for _, part := range strings.FieldsFunc(lower, func(r rune) bool { return r == '-' || r == '_' }) {
		if e, ok := wordEmojis[part]; ok && !avoid[e] {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	return candidates[mathrand.Intn(len(candidates))], true
}

// FallbackEmoji returns a random emoji from a curated, SFW, non-political list,
// preferring ones that are not in avoid. If every choice is in avoid, one is
// still returned (avoiding the avoid set becomes a soft preference).
func FallbackEmoji(avoid map[string]bool) string {
	var allowed []string
	for _, e := range fallbackEmojis {
		if !avoid[e] {
			allowed = append(allowed, e)
		}
	}
	if len(allowed) == 0 {
		return fallbackEmojis[mathrand.Intn(len(fallbackEmojis))]
	}
	return allowed[mathrand.Intn(len(allowed))]
}
