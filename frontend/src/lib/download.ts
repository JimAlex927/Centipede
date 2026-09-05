export function getDownloadFileName(response: any, fallback: string): string {
  const header =
    response?.headers?.["content-disposition"] ??
    response?.headers?.["Content-Disposition"];
  if (!header) return fallback;

  const encodedName =
    header.match(/filename\*=UTF-8''([^;]+)/i)?.[1] ??
    header.match(/filename\s*=\s*"?([^";]+)"?/i)?.[1];
  if (!encodedName) return fallback;

  try {
    return decodeURIComponent(encodedName);
  } catch {
    return encodedName;
  }
}
