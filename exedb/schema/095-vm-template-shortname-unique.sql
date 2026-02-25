-- Add a unique index on vm_shortname for approved templates with a non-empty shortname.
-- This prevents two approved templates from sharing the same shortname, which
-- GetApprovedTemplateByShortname (a :one query) depends on.
CREATE UNIQUE INDEX idx_vm_templates_shortname
    ON vm_templates(vm_shortname)
    WHERE vm_shortname != '' AND status = 'approved';
