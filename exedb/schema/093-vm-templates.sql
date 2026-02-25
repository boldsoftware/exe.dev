-- VM Templates: a store of prompts users can pick from when creating VMs.

CREATE TABLE vm_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    short_description TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'other',
    prompt TEXT NOT NULL,
    icon_url TEXT NOT NULL DEFAULT '',
    screenshot_url TEXT NOT NULL DEFAULT '',
    author_user_id TEXT REFERENCES users(user_id),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    featured INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_vm_templates_status ON vm_templates(status);
CREATE INDEX idx_vm_templates_category ON vm_templates(category);
CREATE INDEX idx_vm_templates_author ON vm_templates(author_user_id);

CREATE TABLE template_ratings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL REFERENCES vm_templates(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    rating INTEGER NOT NULL CHECK (rating >= 1 AND rating <= 5),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(template_id, user_id)
);

CREATE INDEX idx_template_ratings_template ON template_ratings(template_id);
