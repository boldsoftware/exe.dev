1. Never add sleeps to tests.
2. Brevity, brevity, brevity! Do not do weird defaults; have only one way of doing things; refactor relentlessly as necessary.
3. If something doesn't work, propagate the error or exit or crash. Do not have "fallbacks".
4. Do not keep old methods around for "compatibility"; this is a new project and there
   are no compatibility concerns yet.
5. The "predictable" model is a test fixture that lets you specify what a model would say if you said
   a thing. This is useful for interactive testing with a browser, since you don't rely on a model,
   and can fabricate some inputs and outputs. To test things, launch shelley with the relevant flag
   to only expose this model, and use shelley with a browser.
6. Build the UI (`make ui` or `cd ui && npm install && npm run build`) before running Go tests so `ui/dist` exists for the embed.
7. Run Go unit tests with `go test ./server` (or narrower packages while iterating) once the UI bundle is built.
8. To programmatically type into the React message input (e.g., in browser automation), you must use React's internal setter:
   ```javascript
   const input = document.querySelector('[data-testid="message-input"]');
   const nativeInputValueSetter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value").set;
   nativeInputValueSetter.call(input, 'your message');
   input.dispatchEvent(new Event('input', { bubbles: true }));
   ```
   Simply setting `input.value = '...'` won't work because React won't detect the change.
9. Commit your changes before finishing your turn.
10. If you are testing Shelley itself, be aware that you might be running "under" shelley,
  and indiscrimenantly running pkill -f shelley may break things.
11. To test the Shelley UI in a separate instance, build with `make build`, then run on a
    different port with a separate database:
    ```
    ./bin/shelley -config /exe.dev/shelley.json -db /tmp/shelley-test.db serve -port 8002
    ```
    Then use browser tools to navigate to http://localhost:8002/ and interact with the UI.
