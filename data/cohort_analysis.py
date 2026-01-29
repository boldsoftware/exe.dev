#!/usr/bin/env python3
"""
Cohort Analysis for exe.dev users

Produces weekly cohort retention matrix from DuckLake logs.

Usage:
    ./cohort_analysis.py           # Full analysis
    ./cohort_analysis.py --weeks   # Weekly retention only
    ./cohort_analysis.py --days    # Day-level retention only
"""

import subprocess
import json
import sys
from datetime import datetime, timedelta
from collections import defaultdict

DUCKDB_PATH = "/home/exedev/bin/duckdb"
PARQUET_PATH = "data/main/logs/*.parquet"


def run_query(sql: str) -> list[dict]:
    """Run a DuckDB query and return results as list of dicts."""
    cmd = [DUCKDB_PATH, "-json", "-c", sql]
    result = subprocess.run(cmd, capture_output=True, text=True, cwd="/home/exedev/ducklake-importer")
    if result.returncode != 0:
        print(f"Query failed: {result.stderr}", file=sys.stderr)
        sys.exit(1)
    if not result.stdout.strip():
        return []
    return json.loads(result.stdout)


def get_user_activity() -> list[tuple[str, str, str]]:
    """
    Get all (user_id, first_activity_date, activity_date) tuples.
    
    This is the base data we build all analysis on.
    Returns user's first activity date (for cohort assignment) and each activity date.
    """
    sql = f"""
    -- Extract distinct (user_id, date) pairs with their first activity date
    WITH user_dates AS (
        SELECT DISTINCT
            json_extract_string(log_attributes::VARCHAR, '$.user_id') as user_id,
            DATE(timestamp) as activity_date
        FROM read_parquet('{PARQUET_PATH}')
        WHERE json_extract_string(log_attributes::VARCHAR, '$.user_id') IS NOT NULL
          AND json_extract_string(log_attributes::VARCHAR, '$.user_id') != ''
    ),
    user_first AS (
        SELECT 
            user_id,
            MIN(activity_date) as first_activity
        FROM user_dates
        GROUP BY user_id
    )
    SELECT 
        ud.user_id,
        uf.first_activity,
        ud.activity_date
    FROM user_dates ud
    JOIN user_first uf ON ud.user_id = uf.user_id
    ORDER BY uf.first_activity, ud.user_id, ud.activity_date
    """
    
    rows = run_query(sql)
    return [(r['user_id'], r['first_activity'], r['activity_date']) for r in rows]


def get_cohort_week(first_activity: str) -> str:
    """Get the Monday of the week containing the first activity date."""
    dt = datetime.strptime(first_activity, '%Y-%m-%d').date()
    # Monday of that week
    monday = dt - timedelta(days=dt.weekday())
    return monday.strftime('%Y-%m-%d')


def compute_cohort_sizes(activity_data: list[tuple[str, str, str]]) -> dict[str, int]:
    """Count unique users per cohort week."""
    users_per_cohort = defaultdict(set)
    for user_id, first_activity, _ in activity_data:
        cohort_week = get_cohort_week(first_activity)
        users_per_cohort[cohort_week].add(user_id)
    return {cohort: len(users) for cohort, users in users_per_cohort.items()}


def compute_weekly_retention(activity_data: list[tuple[str, str, str]]) -> dict[str, dict[int, int]]:
    """
    Compute retention by week number for each cohort.
    
    Week 0 = cohort week (week of first activity)
    Week 1 = following week
    etc.
    
    Returns: {cohort_week: {week_number: active_user_count}}
    """
    retention = defaultdict(lambda: defaultdict(set))
    
    for user_id, first_activity, activity_date in activity_data:
        cohort_week = get_cohort_week(first_activity)
        cohort_monday = datetime.strptime(cohort_week, '%Y-%m-%d').date()
        activity = datetime.strptime(activity_date, '%Y-%m-%d').date()
        
        # Which week is this activity in?
        days_since_cohort_start = (activity - cohort_monday).days
        week_num = days_since_cohort_start // 7
        
        if week_num >= 0:
            retention[cohort_week][week_num].add(user_id)
    
    # Convert sets to counts
    return {
        cohort: {week: len(users) for week, users in weeks.items()}
        for cohort, weeks in retention.items()
    }


def compute_day_retention(activity_data: list[tuple[str, str, str]]) -> dict[str, dict[int, set]]:
    """
    Compute retention by days since user's first activity.
    
    Day 0 = first activity day
    Day 1 = day after first activity
    etc.
    
    Returns: {cohort_week: {days_since_first: set_of_users}}
    """
    retention = defaultdict(lambda: defaultdict(set))
    
    for user_id, first_activity, activity_date in activity_data:
        cohort_week = get_cohort_week(first_activity)
        first = datetime.strptime(first_activity, '%Y-%m-%d').date()
        activity = datetime.strptime(activity_date, '%Y-%m-%d').date()
        
        days_since = (activity - first).days
        retention[cohort_week][days_since].add(user_id)
    
    return retention


def format_cohort_name(cohort_week: str) -> str:
    """Format cohort week as readable name (e.g., 'Jan 12')."""
    dt = datetime.strptime(cohort_week, '%Y-%m-%d')
    return dt.strftime('%b %d')


def print_retention_matrix(cohort_sizes: dict[str, int], 
                           retention: dict[str, dict[int, int]],
                           max_weeks: int = 6):
    """Print the weekly cohort retention matrix."""
    
    cohorts = sorted(cohort_sizes.keys())
    
    # Header
    header = f"{'Cohort':<12} {'Size':>6}"
    for w in range(max_weeks):
        header += f" {'W'+str(w):>6}"
    print(header)
    print("-" * len(header))
    
    # Data rows
    for cohort in cohorts:
        size = cohort_sizes[cohort]
        row = f"{format_cohort_name(cohort):<12} {size:>6}"
        
        for w in range(max_weeks):
            if w in retention[cohort]:
                active = retention[cohort][w]
                pct = 100.0 * active / size
                row += f" {pct:>5.0f}%"
            else:
                row += f" {'--':>6}"
        
        print(row)


def print_day_retention(activity_data: list[tuple[str, str, str]],
                        cohort_sizes: dict[str, int],
                        days: list[int] = [0, 1, 3, 7, 14, 21, 28]):
    """Print retention at specific day intervals (days since user's first activity)."""
    
    retention = compute_day_retention(activity_data)
    cohorts = sorted(cohort_sizes.keys())
    
    # Header
    header = f"{'Cohort':<12} {'Size':>6}"
    for d in days:
        header += f" {'D'+str(d):>6}"
    print(header)
    print("-" * len(header))
    
    # Data rows
    for cohort in cohorts:
        size = cohort_sizes[cohort]
        row = f"{format_cohort_name(cohort):<12} {size:>6}"
        
        for d in days:
            if d in retention[cohort]:
                active = len(retention[cohort][d])
                pct = 100.0 * active / size
                row += f" {pct:>5.0f}%"
            else:
                row += f" {'--':>6}"
        
        print(row)


def print_summary_stats(activity_data: list[tuple[str, str, str]], 
                        cohort_sizes: dict[str, int]):
    """Print summary statistics."""
    
    total_users = sum(cohort_sizes.values())
    
    # Count users by activity days
    user_dates = defaultdict(set)
    for user_id, _, activity_date in activity_data:
        user_dates[user_id].add(activity_date)
    
    activity_day_counts = defaultdict(int)
    for user_id, dates in user_dates.items():
        activity_day_counts[len(dates)] += 1
    
    one_day_users = activity_day_counts.get(1, 0)
    multi_day_users = total_users - one_day_users
    
    # Get date range
    dates = [d for _, _, d in activity_data]
    min_date = min(dates)
    max_date = max(dates)
    
    print(f"Date Range:      {min_date} to {max_date}")
    print(f"Total Users:     {total_users:,}")
    print(f"Weekly Cohorts:  {len(cohort_sizes)}")
    print(f"One-Day Users:   {one_day_users:,} ({100*one_day_users/total_users:.1f}%)")
    print(f"Multi-Day Users: {multi_day_users:,} ({100*multi_day_users/total_users:.1f}%)")


def main():
    args = sys.argv[1:]
    
    print("=" * 60)
    print("COHORT ANALYSIS - exe.dev")
    print("=" * 60)
    print()
    
    print("Loading user activity data...")
    activity_data = get_user_activity()
    print(f"Loaded {len(activity_data):,} (user, first_activity, date) records")
    print()
    
    # Compute metrics
    cohort_sizes = compute_cohort_sizes(activity_data)
    retention = compute_weekly_retention(activity_data)
    
    # Determine what to print
    show_all = not args or '--all' in args
    show_weeks = show_all or '--weeks' in args
    show_days = show_all or '--days' in args
    show_summary = show_all or '--summary' in args
    
    if show_summary or show_all:
        print("SUMMARY")
        print("-" * 40)
        print_summary_stats(activity_data, cohort_sizes)
        print()
    
    if show_weeks:
        print("WEEKLY COHORT RETENTION MATRIX")
        print("-" * 40)
        print("(Each week starts Monday. W0 = week user signed up.)")
        print()
        print_retention_matrix(cohort_sizes, retention)
        print()
    
    if show_days:
        print("DAY-LEVEL RETENTION (days since user's first activity)")
        print("-" * 40)
        print_day_retention(activity_data, cohort_sizes)
        print()


if __name__ == "__main__":
    main()
