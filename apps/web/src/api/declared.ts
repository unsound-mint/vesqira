/** The name of the import rule that made a declaration, or null when something else did ("user", an adapter, an analysis). ADR 0058. */
export const ruleOf = (declaredBy: string): string | null => (declaredBy.startsWith("rule:") ? declaredBy.slice("rule:".length) : null);
