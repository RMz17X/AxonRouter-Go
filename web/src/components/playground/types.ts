/** One rendered chat message. Conversation state lives in the browser
 * only; the full history is sent with every turn, like a real client. */
export interface PlaygroundMessage {
  role: 'user' | 'assistant'
  content: string
  /** Provider/model + latency + request id line, set when a reply ends. */
  meta?: string
}
