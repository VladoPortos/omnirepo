import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
/**
 * Login page per D-05.
 * Full-page dark background, centered card (max-w-md).
 * framer-motion card fade+scale-in on mount per D-02.
 */
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { motion } from 'framer-motion';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useAuth } from '@/hooks/useAuth';
import { ApiError } from '@/api/client';
export function LoginPage() {
    const navigate = useNavigate();
    const { login } = useAuth();
    const [loginValue, setLoginValue] = useState('');
    const [password, setPassword] = useState('');
    const [error, setError] = useState('');
    const handleSubmit = async (e) => {
        e.preventDefault();
        setError('');
        try {
            const result = await login.mutateAsync({
                login: loginValue,
                password,
            });
            if (result.must_change_password) {
                navigate('/change-password');
            }
            else {
                navigate('/');
            }
        }
        catch (err) {
            if (err instanceof ApiError) {
                if (err.status === 401 || err.status === 403) {
                    setError('Invalid login or password. Please try again.');
                }
                else {
                    setError('Unable to reach the server. Check your connection and try again.');
                }
            }
            else {
                setError('Unable to reach the server. Check your connection and try again.');
            }
        }
    };
    return (_jsx("div", { className: "flex min-h-screen items-center justify-center bg-background p-4", children: _jsx(motion.div, { initial: { opacity: 0, scale: 0.95 }, animate: { opacity: 1, scale: 1 }, transition: { duration: 0.15, ease: 'easeOut' }, children: _jsxs(Card, { className: "w-full max-w-md", children: [_jsxs(CardHeader, { className: "text-center", children: [_jsx("div", { className: "mx-auto flex size-12 items-center justify-center rounded-lg bg-primary text-primary-foreground mb-4", children: _jsx("span", { className: "text-xl font-semibold", children: "O" }) }), _jsx(CardTitle, { className: "text-2xl", children: "Sign in to OmniRepo" })] }), _jsx(CardContent, { children: _jsxs("form", { onSubmit: handleSubmit, className: "space-y-4", children: [error && (_jsx("div", { className: "rounded-md bg-destructive/10 p-3 text-sm text-destructive", children: error })), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "login", children: "Login" }), _jsx(Input, { id: "login", type: "text", value: loginValue, onChange: (e) => setLoginValue(e.target.value), placeholder: "Enter your login", autoComplete: "username", autoFocus: true, required: true })] }), _jsxs("div", { className: "space-y-2", children: [_jsx(Label, { htmlFor: "password", children: "Password" }), _jsx(Input, { id: "password", type: "password", value: password, onChange: (e) => setPassword(e.target.value), placeholder: "Enter your password", autoComplete: "current-password", required: true })] }), _jsx(Button, { type: "submit", className: "w-full", disabled: login.isPending, children: login.isPending ? 'Signing in...' : 'Sign In' })] }) })] }) }) }));
}
