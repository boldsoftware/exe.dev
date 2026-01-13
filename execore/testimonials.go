package execore

// Testimonial represents a user testimonial for the front page.
type Testimonial struct {
	Quote    string
	Author   string
	Link     string // optional source link
	Approved bool
}

// testimonials is the list of all testimonials.
var testimonials = []Testimonial{
	{
		Quote:    "I just vibecoded with exe.dev and Opus 4.5 a backoffice for our FIPS 140 validation, with a separate view for the lab (where they can also upload test vectors), public links for clients, and guided scripts for testing.\n\nI have not looked at the code once. It works great.\n\nI am... processing this.",
		Author:   "Filippo Valsorda",
		Link:     "https://abyssdomain.expert/@filippo/115826635660720358",
		Approved: true,
	},
	{
		Quote:    "Shelley is seriously incredible, I use a lot of AI dev agents and y'all are really not talking about Shelley enough",
		Author:   "XplsosivesX, Discord",
		Approved: true,
	},
	{
		Quote:    "That must be worst website ever made.",
		Author:   "Anonymous, Hacker News",
		Link:     "https://news.ycombinator.com/item?id=46397609",
		Approved: true,
	},
	{
		Quote:    "Shelley needs advertised more in your docs and website. It has got me hooked! it was amazing to prototype an app idea within only a few minutes from my phone. it was one of those ideas that had been floating around in my head for years but had never found time for",
		Author:   "Pertempto, Discord",
		Approved: false,
	},
	{
		Quote:    "Been using it for just over a week now. Really falling in love with it. Even with out AI coding features, I'm not sure how I'd do local development without it.",
		Author:   "Mark Roddy",
		Link:     "https://bsky.app/profile/launchit.ai/post/3marf3eofgk2k",
		Approved: true,
	},
	{
		Quote:    "Daily appreciation for building this - exe.dev and Shelley are amazing! My friends and I (and my dad) have been churning out apps every day!",
		Author:   "consti, Discord",
		Approved: true,
	},
	{
		Quote:    "Seriously don't die. I haven't found a service I could code on my phone from like this. It's amazing. Now I'm programming remote servers. Stopped using copilot...",
		Author:   "Asim, Discord",
		Approved: true,
	},
}

// ApprovedTestimonials returns all approved testimonials.
func ApprovedTestimonials() []Testimonial {
	var approved []Testimonial
	for _, t := range testimonials {
		if t.Approved {
			approved = append(approved, t)
		}
	}
	return approved
}

// AllTestimonials returns all testimonials (for the debug page).
func AllTestimonials() []Testimonial {
	return testimonials
}
