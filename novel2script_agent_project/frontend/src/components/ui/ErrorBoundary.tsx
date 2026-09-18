import { Component, type ErrorInfo, type ReactNode } from "react";

export class ErrorBoundary extends Component<{ children: ReactNode; title: string; resetKey?: string }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };
  static getDerivedStateFromError(error: Error) { return { error }; }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error("UI section failed", error, info.componentStack); }
  componentDidUpdate(previous: Readonly<{ children: ReactNode; title: string; resetKey?: string }>) {
    if (this.state.error && previous.resetKey !== this.props.resetKey) this.setState({ error: null });
  }
  render() {
    if (!this.state.error) return this.props.children;
    return <section className="section-error" role="alert"><h2>{this.props.title}</h2><p>这一部分暂时无法显示，其他项目内容未受影响。</p><button onClick={() => this.setState({ error: null })} type="button">重新加载此区域</button></section>;
  }
}
