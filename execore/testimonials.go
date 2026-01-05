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
