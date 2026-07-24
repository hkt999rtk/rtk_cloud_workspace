function serializableError(error) {
  if (!error) return undefined;
  return {
    name: error.name,
    message: error.message,
    code: error.code,
    stack: error.stack,
  };
}

export default async function* testEventReporter(source) {
  for await (const event of source) {
    if (event.type !== "test:start" && event.type !== "test:pass" && event.type !== "test:fail") {
      continue;
    }
    const details = event.data.details ?? {};
    yield `${JSON.stringify({
      schema_version: 1,
      event: event.type.slice("test:".length),
      name: event.data.name,
      file: event.data.file,
      line: event.data.line,
      column: event.data.column,
      nesting: event.data.nesting,
      test_type: details.type,
      duration_ms: details.duration_ms,
      skip: details.skip,
      todo: details.todo,
      error: serializableError(details.error),
      timestamp: new Date().toISOString(),
    })}\n`;
  }
}
