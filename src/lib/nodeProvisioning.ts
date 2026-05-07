export function parseDnsList(raw: string): string[] {
  return raw.split(',').map((part) => part.trim()).filter(Boolean)
}
