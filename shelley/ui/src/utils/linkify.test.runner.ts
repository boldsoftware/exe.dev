// Test runner for linkify tests - can be run with tsx/ts-node
import { runTests } from "./linkify.test";

const { passed, failed, failures } = runTests();

console.log(`\nLinkify Tests: ${passed} passed, ${failed} failed\n`);

if (failures.length > 0) {
  console.log("Failures:");
  for (const f of failures) {
    console.log(f);
    console.log("");
  }
  process.exit(1);
}

console.log("All tests passed!");
process.exit(0);
