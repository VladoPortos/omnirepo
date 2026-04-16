import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * 404 page per UI spec General copywriting.
 */
import { Link } from 'react-router-dom';
import { Button } from '@/components/ui/button';
export function NotFoundPage() {
    return (_jsxs("div", { className: "flex min-h-screen flex-col items-center justify-center bg-background p-4 text-center", children: [_jsx("h1", { className: "text-4xl font-semibold text-foreground mb-2", children: "Page Not Found" }), _jsx("p", { className: "text-muted-foreground mb-6 max-w-md", children: "The page you're looking for doesn't exist or has been moved." }), _jsx(Button, { render: _jsx(Link, { to: "/" }), children: "Go to Dashboard" })] }));
}
