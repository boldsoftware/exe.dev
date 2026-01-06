package execore

// Testimonial represents a user testimonial for the front page.
type Testimonial struct {
	// HTML is the testimonial content as HTML.
	HTML string
	// Approved controls whether this testimonial is shown on the front page.
	Approved bool
}

// testimonials is the list of all testimonials.
var testimonials = []Testimonial{
	{
		HTML: `I just vibecoded with exe.dev and Opus 4.5 a backoffice for our FIPS 140 validation, with a separate view for the lab (where they can also upload test vectors), public links for clients, and guided scripts for testing.<br><br>
I have not looked at the code once. It works great.<br><br>
I am... processing this.<br><br>
<span style="color: #666;">&mdash; <a href="https://abyssdomain.expert/@filippo/115826635660720358">Filippo Valsorda</a></span>`,
		Approved: true,
	},
	{
		HTML: `Shelley is seriously incredible, I use a lot of AI dev agents and y'all are really not talking about Shelley enough<br><br>
<span style="color: #666;">&mdash; XplsosivesX, Discord</span>`,
		Approved: true,
	},
	{
		HTML: `That must be worst website ever made.<br><br>
<span style="color: #666;">&mdash; <a href="https://news.ycombinator.com/item?id=46397609">Anonymous, Hacker News</a></span>`,
		Approved: true,
	},
	{
		HTML: `Shelley needs advertised more in your docs and website. It has got me hooked! it was amazing to prototype an app idea within only a few minutes from my phone. it was one of those ideas that had been floating around in my head for years but had never found time for<br><br>
<span style="color: #666;">&mdash; Pertempto, Discord</span>`,
		Approved: false,
	},
	{
		HTML: `Been using it for just over a week now. Really falling in love with it. Even with out AI coding features, I'm not sure how I'd do local development without it.<br><br>
<span style="color: #666;">&mdash; <a href="https://bsky.app/profile/launchit.ai/post/3marf3eofgk2k">Mark Roddy</a></span>`,
		Approved: true,
	},
	{
		HTML: `Daily appreciation for building this - exe.dev and Shelley are amazing! My friends and I (and my dad) have been churning out apps every day!<br><br>
<span style="color: #666;">&mdash; conti, Discord</span>`,
		Approved: true,
	},
	{
		HTML: `Seriously don't die. I haven't found a service I could code on my phone from like this. It's amazing. Now I'm programming remote servers. Stopped using copilot...<br><br>
<span style="color: #666;">&mdash; Asim, Discord</span>`,
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
