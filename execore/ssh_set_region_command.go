package execore

import (
	"context"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/region"
)

func (ss *SSHServer) handleSetRegionCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		available := ss.server.availableRegionsForUser(ctx, cc.User.ID, cc.User.Region)
		var codes []string
		for _, r := range available {
			codes = append(codes, r.Code)
		}
		return cc.Errorf("usage: set-region <region-code>\r\navailable: %s", strings.Join(codes, ", "))
	}

	code := strings.ToLower(cc.Args[0])

	available := ss.server.availableRegionsForUser(ctx, cc.User.ID, cc.User.Region)
	var chosen *region.Region
	for _, r := range available {
		if r.Code == code {
			r := r
			chosen = &r
			break
		}
	}
	if chosen == nil {
		var codes []string
		for _, r := range available {
			codes = append(codes, r.Code)
		}
		return cc.Errorf("region %q is not available; choose from: %s", code, strings.Join(codes, ", "))
	}

	if err := withTx1(ss.server, ctx, (*exedb.Queries).SetUserRegion, exedb.SetUserRegionParams{
		UserID: cc.User.ID,
		Region: chosen.Code,
	}); err != nil {
		return cc.Errorf("failed to update region: %v", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]string{"region": chosen.Code, "region_display": chosen.Display})
		return nil
	}
	cc.Writeln("Region set to %s (%s)", chosen.Code, chosen.Display)
	return nil
}
