/**
 * What to tell an operator when reading from the cluster failed, by the HTTP
 * status the read ended with. One generic "Cannot reach the Kubernetes API"
 * for everything sent people to check the proxy when the real answer was "your
 * role may not read this" (403) or "this cluster does not serve it" (404).
 */
export interface LoadErrorCopy {
  title: string
  hint: string
}

export function describeLoadError(status: number | undefined): LoadErrorCopy {
  switch (status) {
    case 401:
      return {
        title: 'Your session has expired',
        hint: 'Sign in again. The console reads the cluster under your own identity, and it was not accepted.',
      }
    case 403:
      return {
        title: 'Your role is not allowed to read this',
        hint: 'The cluster refused the read for your account. Ask an administrator whether your role (viewer, editor or admin) should see it.',
      }
    case 404:
      return {
        title: 'This is not installed on this cluster',
        hint: 'The API does not serve this resource. The component behind it may not be deployed, or its CRD is missing.',
      }
    case 429:
    case 500:
    case 502:
    case 503:
    case 504:
      return {
        title: 'The Kubernetes API is not answering properly',
        hint: 'It returned an error or is overloaded. Try again in a moment; if it persists, check the apiserver and the in-pod frame-uiproxy.',
      }
    default:
      return {
        title: 'Cannot reach the Kubernetes API',
        hint: 'The UI reaches /api through the in-pod frame-uiproxy, under your own identity. Check that the proxy is running; local development is covered in docs/development.md.',
      }
  }
}
