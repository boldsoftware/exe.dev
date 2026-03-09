"""End-to-end demo: proxy-backed Python objects explored by an RLM agent.

Creates a multi-level company hierarchy as proxy objects, exposes them
to an RLM agent via UDS bridge, and asks the agent to discover and
analyze the structure. Logs all proxy access to confirm plumbing works.
"""

import json
import logging
import os
import sys
import time

# Project root on path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import dspy

from panopticon.mux import MuxServer
from panopticon.proxy import ProxyObject, ProxyRegistry
from deps.dspy.predict.rlm import RLM

# =============================================================================
# Logging setup — verbose enough to see every proxy access
# =============================================================================

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("demo")


# =============================================================================
# Logging ProxyRegistry — wraps resolve_getattr to log every access
# =============================================================================


class LoggingProxyRegistry(ProxyRegistry):
    """ProxyRegistry that logs every attribute resolution."""

    def __init__(self):
        super().__init__()
        self._access_log: list[dict] = []

    def resolve_getattr(self, proxy_id: str, attr: str) -> dict:
        start = time.monotonic()
        result = super().resolve_getattr(proxy_id, attr)
        elapsed_ms = (time.monotonic() - start) * 1000

        entry = {
            "proxy_id": proxy_id,
            "attr": attr,
            "result_type": result.get("type", "?"),
            "elapsed_ms": round(elapsed_ms, 2),
        }
        self._access_log.append(entry)

        # Summarize the result for logging
        rtype = result.get("type")
        if rtype == "concrete":
            val = result["value"]
            summary = repr(val)[:80]
        elif rtype == "proxy":
            summary = f"-> {result.get('type_name')}({result.get('proxy_id')})"
        elif rtype == "proxy_list":
            summary = f"[{len(result['items'])} items]"
        elif rtype == "proxy_dict":
            summary = f"{{{len(result['items'])} entries}}"
        else:
            summary = str(result)[:80]

        log.info(f"PROXY ACCESS: {proxy_id}.{attr} => {rtype}: {summary} ({elapsed_ms:.1f}ms)")
        return result

    def dump_access_log(self):
        log.info(f"\n{'='*70}")
        log.info(f"PROXY ACCESS LOG — {len(self._access_log)} total accesses")
        log.info(f"{'='*70}")
        for i, entry in enumerate(self._access_log, 1):
            log.info(f"  {i:3d}. {entry['proxy_id']}.{entry['attr']} => {entry['result_type']}")
        log.info(f"{'='*70}\n")


# =============================================================================
# Proxy-backed domain objects — a company with departments and employees
# =============================================================================


class Project(ProxyObject):
    def __init__(self, project_id: str, name: str, status: str, description: str):
        super().__init__(
            proxy_id=f"project_{project_id}",
            type_name="Project",
            doc="A project with a name, status, and description.",
            dir_attrs=["name", "status", "description"],
            attr_docs={
                "name": "Project name",
                "status": "Current status (active/completed/planned)",
                "description": "What this project is about",
            },
        )
        self.name = name
        self.status = status
        self.description = description


class Employee(ProxyObject):
    def __init__(
        self,
        emp_id: str,
        name: str,
        title: str,
        email: str,
        reports: list | None = None,
        projects: list | None = None,
    ):
        super().__init__(
            proxy_id=f"emp_{emp_id}",
            type_name="Employee",
            doc="An employee with contact info, direct reports, and project assignments.",
            dir_attrs=["name", "title", "email", "reports", "projects"],
            attr_docs={
                "name": "Full name",
                "title": "Job title",
                "email": "Email address",
                "reports": "List of direct reports (employees)",
                "projects": "List of assigned projects",
            },
        )
        self.name = name
        self.title = title
        self.email = email
        self.reports = reports or []
        self.projects = projects or []


class Department(ProxyObject):
    def __init__(
        self,
        dept_id: str,
        name: str,
        head: Employee,
        employees: list[Employee],
        budget: float,
    ):
        super().__init__(
            proxy_id=f"dept_{dept_id}",
            type_name="Department",
            doc="A department with a head, employee roster, and budget.",
            dir_attrs=["name", "head", "employees", "budget"],
            attr_docs={
                "name": "Department name",
                "head": "Department head (an Employee)",
                "employees": "All employees in this department",
                "budget": "Annual budget in USD",
            },
        )
        self.name = name
        self.head = head
        self.employees = employees
        self.budget = budget


class Company(ProxyObject):
    def __init__(
        self,
        name: str,
        ceo: Employee,
        departments: list[Department],
        founded: int,
        motto: str,
    ):
        super().__init__(
            proxy_id="company",
            type_name="Company",
            doc="A company with departments, employees, and projects.",
            dir_attrs=["name", "ceo", "departments", "founded", "motto"],
            attr_docs={
                "name": "Company name",
                "ceo": "Chief Executive Officer (an Employee)",
                "departments": "List of departments",
                "founded": "Year the company was founded",
                "motto": "Company motto",
            },
        )
        self.name = name
        self.ceo = ceo
        self.departments = departments
        self.founded = founded
        self.motto = motto


# =============================================================================
# Build the demo data
# =============================================================================


def build_company() -> Company:
    """Construct a multi-level company hierarchy."""

    # Projects
    p_search = Project("search", "SearchV2", "active", "Next-gen search engine with ML ranking")
    p_ads = Project("ads", "AdOptimizer", "active", "Real-time ad bidding platform")
    p_infra = Project("infra", "CloudMigration", "active", "Migrate on-prem to Kubernetes")
    p_security = Project("sec", "ZeroTrust", "planned", "Zero-trust security architecture")
    p_mobile = Project("mobile", "MobileApp", "completed", "iOS and Android app v2.0")
    p_data = Project("data", "DataLake", "active", "Unified analytics data lake")

    # Engineering employees
    eng_alice = Employee("alice", "Alice Chen", "Staff Engineer", "alice@acme.com",
                         projects=[p_search, p_infra])
    eng_bob = Employee("bob", "Bob Kim", "Senior Engineer", "bob@acme.com",
                       projects=[p_search])
    eng_carol = Employee("carol", "Carol Patel", "Engineer", "carol@acme.com",
                         projects=[p_infra, p_security])
    eng_dave = Employee("dave", "Dave Lopez", "Engineering Manager", "dave@acme.com",
                        reports=[eng_alice, eng_bob, eng_carol],
                        projects=[p_search, p_infra])

    # Marketing employees
    mkt_eve = Employee("eve", "Eve Johnson", "Marketing Lead", "eve@acme.com",
                       projects=[p_ads, p_mobile])
    mkt_frank = Employee("frank", "Frank Wu", "Content Strategist", "frank@acme.com",
                         projects=[p_ads])

    # Data Science employees
    ds_grace = Employee("grace", "Grace Okafor", "Data Scientist", "grace@acme.com",
                        projects=[p_data, p_search])
    ds_hank = Employee("hank", "Hank Müller", "ML Engineer", "hank@acme.com",
                       projects=[p_data])
    ds_ivy = Employee("ivy", "Ivy Tanaka", "DS Manager", "ivy@acme.com",
                      reports=[ds_grace, ds_hank],
                      projects=[p_data])

    # CEO
    ceo = Employee("ceo", "Jordan Rivera", "CEO", "jordan@acme.com",
                   reports=[eng_dave, mkt_eve, ds_ivy])

    # Departments
    engineering = Department("eng", "Engineering", eng_dave,
                             [eng_dave, eng_alice, eng_bob, eng_carol], 2_500_000.0)
    marketing = Department("mkt", "Marketing", mkt_eve,
                           [mkt_eve, mkt_frank], 800_000.0)
    data_science = Department("ds", "Data Science", ds_ivy,
                              [ds_ivy, ds_grace, ds_hank], 1_200_000.0)

    return Company("Acme Corp", ceo, [engineering, marketing, data_science], 2015, "Building the future, one commit at a time")


# =============================================================================
# Main
# =============================================================================


def main():
    log.info("Building company proxy objects...")
    company = build_company()

    log.info("Setting up proxy registry with logging...")
    registry = LoggingProxyRegistry()
    registry.register(company)

    log.info("Starting UDS mux server...")
    with MuxServer(registry) as mux:
        log.info(f"MuxServer listening on {mux.socket_path}")

        # Configure dspy with Claude
        lm = dspy.LM("anthropic/claude-sonnet-4-20250514", max_tokens=4096)
        dspy.configure(lm=lm)

        # Create the RLM module (vendored version with proxy support)
        rlm = RLM(
            "company -> report",
            max_iterations=15,
            max_llm_calls=5,
            uds_path=mux.socket_path,
            verbose=True,
        )

        log.info("Running RLM agent...")
        log.info("=" * 70)

        result = rlm(company=company)

        log.info("=" * 70)
        log.info("RLM RESULT:")
        log.info(result.report)

        # Dump the access log
        registry.dump_access_log()

        log.info("Done!")


if __name__ == "__main__":
    main()
