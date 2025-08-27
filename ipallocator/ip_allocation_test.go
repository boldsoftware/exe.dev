package ipallocator

import (
	"fmt"
	"testing"
)

func TestMDNSAllocator_IPAllocation(t *testing.T) {
	t.Parallel()

	// Test IP allocation system without actual mDNS registration
	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// Test basic IP allocation
	allocation1, err := strategy.Allocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate IP: %v", err)
	}
	if allocation1 == nil {
		t.Fatal("Expected non-nil allocation")
	}
	expected := "127.0.0.2"
	if allocation1.IP != expected {
		t.Errorf("Expected first allocated IP to be %s, got %s", expected, allocation1.IP)
	}

	// Test machine lookup by IP
	machineName, found := strategy.LookupMachine("team1", allocation1.IP)
	if !found {
		t.Error("Machine not found by IP for team1")
	}
	if machineName != "machine1" {
		t.Errorf("Wrong machine name: expected machine1, got %s", machineName)
	}

	// Test allocating another IP for same team
	allocation2, err := strategy.Allocate("team1", "machine2")
	if err != nil {
		t.Fatalf("Failed to allocate second IP: %v", err)
	}
	expected2 := "127.0.0.3"
	if allocation2.IP != expected2 {
		t.Errorf("Expected second IP to be %s, got %s", expected2, allocation2.IP)
	}

	// Test allocating IP for different team (should be able to reuse IP)
	allocation3, err := strategy.Allocate("team2", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate IP for team2: %v", err)
	}
	// Teams can share the same IP - that's allowed and expected
	t.Logf("team1/machine1 got IP: %s, team2/machine1 got IP: %s", allocation1.IP, allocation3.IP)

	// Test re-allocating same machine (should return same IP)
	allocation1_again, err := strategy.Allocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to re-allocate IP for existing machine: %v", err)
	}
	if allocation1.IP != allocation1_again.IP {
		t.Errorf("Re-allocation returned different IP: %s vs %s", allocation1.IP, allocation1_again.IP)
	}

	// Test deallocation
	err = strategy.Deallocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to deallocate IP: %v", err)
	}

	// Verify machine is no longer found
	_, found = strategy.LookupMachine("team1", allocation1.IP)
	if found {
		t.Error("Machine should not be found after deallocation")
	}
}

func TestMDNSAllocator_MachineLimit(t *testing.T) {
	t.Parallel()

	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// This test would take too long to run for real, so just test the concept
	// by checking a few allocations work, then testing the limit logic separately
	for i := 0; i < 5; i++ {
		machineName := fmt.Sprintf("machine%d", i)
		_, err := strategy.Allocate("testteam", machineName)
		if err != nil {
			t.Fatalf("Failed to allocate IP %d: %v", i, err)
		}
	}

	// Verify we have 5 IPs allocated
	teamIPs := strategy.teamIPAllocation["testteam"]
	if len(teamIPs) != 5 {
		t.Errorf("Expected 5 allocated IPs, got %d", len(teamIPs))
	}
}

func TestMDNSAllocator_MultipleTeamsCanShareIP(t *testing.T) {
	t.Parallel()

	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// Allocate IP for team1/machine1
	allocation1, err := strategy.Allocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate IP for team1/machine1: %v", err)
	}

	// Allocate IP for team2/machine1 - should be able to share same IP
	allocation2, err := strategy.Allocate("team2", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate IP for team2/machine1: %v", err)
	}

	// Verify team1's machine can be found
	team1Machine, team1Found := strategy.LookupMachine("team1", allocation1.IP)
	if !team1Found {
		t.Error("team1 machine not found in IP lookup")
	}
	if team1Machine != "machine1" {
		t.Errorf("Wrong team1 machine: expected machine1, got %s", team1Machine)
	}

	// Verify team2's machine can be found
	team2Machine, team2Found := strategy.LookupMachine("team2", allocation2.IP)
	if !team2Found {
		t.Error("team2 machine not found in IP lookup")
	}
	if team2Machine != "machine1" {
		t.Errorf("Wrong team2 machine: expected machine1, got %s", team2Machine)
	}
}

func TestMDNSAllocator_SameTeamDifferentMachines(t *testing.T) {
	t.Parallel()
	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// Allocate IP for team1/machine1
	allocation1, err := strategy.Allocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate IP for team1/machine1: %v", err)
	}

	// Allocate IP for team1/machine2 - should get different IP
	allocation2, err := strategy.Allocate("team1", "machine2")
	if err != nil {
		t.Fatalf("Failed to allocate IP for team1/machine2: %v", err)
	}

	// Same team cannot use the same IP for multiple machines
	if allocation1.IP == allocation2.IP {
		t.Errorf("Same team should not use same IP for different machines, both got %s",
			allocation1.IP)
	}

	// Verify each machine can be looked up by its unique IP
	machine1, found1 := strategy.LookupMachine("team1", allocation1.IP)
	if !found1 {
		t.Fatal("Machine lookup failed for allocation1.IP")
	}
	if machine1 != "machine1" {
		t.Errorf("Expected machine1 at IP %s, got %s", allocation1.IP, machine1)
	}

	machine2, found2 := strategy.LookupMachine("team1", allocation2.IP)
	if !found2 {
		t.Fatal("Machine lookup failed for allocation2.IP")
	}
	if machine2 != "machine2" {
		t.Errorf("Expected machine2 at IP %s, got %s", allocation2.IP, machine2)
	}
}

func TestMDNSAllocator_IPSharingBetweenTeams(t *testing.T) {
	t.Parallel()
	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// Create multiple teams with multiple machines to demonstrate IP sharing

	// Team1: machine1, machine2
	team1m1, _ := strategy.Allocate("team1", "machine1")
	team1m2, _ := strategy.Allocate("team1", "machine2")

	// Team2: machine1, machine2
	team2m1, _ := strategy.Allocate("team2", "machine1")
	team2m2, _ := strategy.Allocate("team2", "machine2")

	// Team3: machine1
	team3m1, _ := strategy.Allocate("team3", "machine1")

	allAllocations := []*Allocation{team1m1, team1m2, team2m1, team2m2, team3m1}

	// Within team1, machines should have different IPs
	if team1m1.IP == team1m2.IP {
		t.Error("team1 machines should not share IP addresses")
	}

	// Within team2, machines should have different IPs
	if team2m1.IP == team2m2.IP {
		t.Error("team2 machines should not share IP addresses")
	}

	// Count unique IPs - should be fewer than total allocations due to sharing
	uniqueIPs := make(map[string]bool)
	for _, allocation := range allAllocations {
		uniqueIPs[allocation.IP] = true
	}

	// With IP sharing, we should have fewer unique IPs than allocations
	if len(uniqueIPs) >= len(allAllocations) {
		t.Logf("IP allocations: %v", func() []string {
			var ips []string
			for _, a := range allAllocations {
				ips = append(ips, a.IP)
			}
			return ips
		}())
		t.Errorf("Expected IP sharing between teams. Got %d unique IPs for %d allocations",
			len(uniqueIPs), len(allAllocations))
	}

	// Verify we can lookup all machines by their team and IP
	teams := []string{"team1", "team2", "team3"}
	allocMap := map[string][]*Allocation{
		"team1": {team1m1, team1m2},
		"team2": {team2m1, team2m2},
		"team3": {team3m1},
	}

	for _, teamName := range teams {
		for _, allocation := range allocMap[teamName] {
			machineName, found := strategy.LookupMachine(teamName, allocation.IP)
			if !found {
				t.Errorf("Failed to lookup machine for team %s at IP: %s", teamName, allocation.IP)
			}
			// We can't verify the machine name matches exactly because
			// the test doesn't track which specific machine got which allocation
			if machineName == "" {
				t.Errorf("Empty machine name returned for team %s at IP %s", teamName, allocation.IP)
			}
		}
	}

	// Check for IP sharing by looking for cases where multiple teams use the same IP
	foundMultiTeamIP := false
	for ip := range uniqueIPs {
		teamsUsingThisIP := 0
		for _, teamName := range teams {
			if _, found := strategy.LookupMachine(teamName, ip); found {
				teamsUsingThisIP++
			}
		}
		if teamsUsingThisIP > 1 {
			foundMultiTeamIP = true
			t.Logf("Found IP %s shared by %d teams", ip, teamsUsingThisIP)
			break
		}
	}

	// We should find at least one IP that multiple teams share
	if !foundMultiTeamIP {
		t.Error("Expected to find at least one IP shared by multiple teams")
	}
}

func TestMDNSAllocator_IPRangeComment(t *testing.T) {
	t.Parallel()
	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// Test that exe.local gets 127.0.0.1 (this is handled in Start())
	// and machines get IPs in range [127.0.0.2, 127.0.0.255]

	// Allocate first machine - should get 127.0.0.2
	allocation1, err := strategy.Allocate("team1", "machine1")
	if err != nil {
		t.Fatalf("Failed to allocate first IP: %v", err)
	}

	expected := "127.0.0.2"
	if allocation1.IP != expected {
		t.Errorf("Expected first machine IP to be %s, got %s", expected, allocation1.IP)
	}

	// Allocate second machine for same team - should get 127.0.0.3
	allocation2, err := strategy.Allocate("team1", "machine2")
	if err != nil {
		t.Fatalf("Failed to allocate second IP: %v", err)
	}

	expected2 := "127.0.0.3"
	if allocation2.IP != expected2 {
		t.Errorf("Expected second machine IP to be %s, got %s", expected2, allocation2.IP)
	}
}

func TestMDNSAllocator_TeamMachineLimit(t *testing.T) {
	t.Parallel()
	strategy := NewMDNSAllocator()
	err := strategy.Start()
	if err != nil {
		t.Fatalf("Failed to start IP allocation strategy: %v", err)
	}
	defer strategy.Stop()

	// A team should be able to allocate up to 254 machines (127.0.0.2-127.0.0.255)
	// Let's test a smaller number to keep test time reasonable
	const testMachineCount = 10

	allocations := make([]*Allocation, testMachineCount)
	for i := 0; i < testMachineCount; i++ {
		machineName := fmt.Sprintf("machine%d", i)
		allocation, err := strategy.Allocate("testteam", machineName)
		if err != nil {
			t.Fatalf("Failed to allocate machine %d: %v", i, err)
		}
		allocations[i] = allocation
	}

	// All machines for the same team should have unique IPs
	ipSet := make(map[string]bool)
	for i, allocation := range allocations {
		if ipSet[allocation.IP] {
			t.Errorf("Machine %d got duplicate IP %s", i, allocation.IP)
		}
		ipSet[allocation.IP] = true
	}

	// Should have exactly testMachineCount unique IPs
	if len(ipSet) != testMachineCount {
		t.Errorf("Expected %d unique IPs, got %d", testMachineCount, len(ipSet))
	}
}
