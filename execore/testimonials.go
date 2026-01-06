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
		HTML: `<strong>Filippo Valsorda</strong><br>
<span style="color: #666;">@filippo.abyssdomain.expert</span><br><br>
I just vibecoded with exe.dev and Opus 4.5 a backoffice for our FIPS 140 validation, with a separate view for the lab (where they can also upload test vectors), public links for clients, and guided scripts for testing.<br><br>
I have not looked at the code once. It works great.<br><br>
I am... processing this.`,
		Approved: true,
	},
	{
		HTML: `<strong>XplsosivesX</strong><br>
<span style="color: #666;">exe.dev Discord</span><br><br>
Shelley is seriously incredible, I use a lot of AI dev agents and y'all are really not talking about Shelley enough`,
		Approved: true,
	},
	{
		HTML: `<strong>Anonymous</strong><br>
<span style="color: #666;"><a href="https://news.ycombinator.com/item?id=46397609">Hacker News</a></span><br><br>
That must be worst website ever made.`,
		Approved: true,
	},
	{
		HTML: `<strong>Pertempto</strong><br>
<span style="color: #666;">Discord</span><br><br>
Shelley needs advertised more in your docs and website. It has got me hooked! it was amazing to prototype an app idea within only a few minutes from my phone. it was one of those ideas that had been floating around in my head for years but had never found time for`,
		Approved: false,
	},
	{
		HTML: `<strong>Mark Roddy</strong><br>
<span style="color: #666;"><a href="https://bsky.app/profile/launchit.ai/post/3marf3eofgk2k">@launchit.ai</a></span><br><br>
Been using it for just over a week now. Really falling in love with it. Even with out AI coding features, I'm not sure how I'd do local development without it.`,
		Approved: false,
	},
	{
		HTML: `<strong>conti</strong><br>
<span style="color: #666;">Discord</span><br><br>
Daily appreciation for building this - exe.dev and Shelley are amazing! My friends and I (and my dad) have been churning out apps every day!`,
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
